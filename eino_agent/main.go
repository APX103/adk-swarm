package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/google/uuid"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	openai "github.com/cloudwego/eino-ext/components/model/openai"
)

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// ---------- Weather Tool ----------

type WeatherInput struct {
	City string `json:"city" jsonschema:"description=城市名称，如 北京、上海、东京"`
}

type WeatherOutput struct {
	Report string `json:"report"`
}

func getWeather(_ context.Context, input *WeatherInput) (*WeatherOutput, error) {
	city := input.City
	if city == "" {
		city = "未知"
	}
	seed := 0
	for _, c := range city {
		seed += int(c)
	}
	seed = seed % 4

	conditions := []string{"晴", "多云", "小雨", "雷阵雨"}
	temps := []int{28, 24, 19, 31}
	humidity := 50 + seed*10
	wind := seed + 2

	report := fmt.Sprintf("%s：%s，气温 %d°C，湿度 %d%%，风力 %d 级。",
		city, conditions[seed], temps[seed], humidity, wind)
	return &WeatherOutput{Report: report}, nil
}

// ---------- Agent Card (A2A v0.3.0 compatible) ----------

type AgentCard struct {
	Name              string        `json:"name"`
	Description       string        `json:"description"`
	URL               string        `json:"url"`
	Version           string        `json:"version"`
	ProtocolVersion   string        `json:"protocolVersion"`
	PreferredTransport string       `json:"preferredTransport"`
	Capabilities      *Capabilities `json:"capabilities"`
	DefaultInputModes  []string     `json:"defaultInputModes"`
	DefaultOutputModes []string     `json:"defaultOutputModes"`
	Skills            []Skill       `json:"skills"`
	SupportsAuthExt   bool          `json:"supportsAuthenticatedExtendedCard"`
}

type Capabilities struct {
	Streaming  interface{} `json:"streaming"`
	Extensions interface{} `json:"extensions"`
}

type Skill struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Tags        []string `json:"tags"`
	Examples    []string `json:"examples"`
}

func buildAgentCard(externalURL string) AgentCard {
	return AgentCard{
		Name:        "eino_agent",
		Description: "基于 CloudWeGo Eino 框架的 Go Agent，支持天气查询（MCP 风格工具）、上下文压缩和会话管理。可以查询任意城市的天气状况。",
		URL:         externalURL,
		Version:     "1.0.0",
		ProtocolVersion:    "0.3.0",
		PreferredTransport: "JSONRPC",
		Capabilities: &Capabilities{
			Streaming:  nil,
			Extensions: nil,
		},
		DefaultInputModes:  []string{"text/plain"},
		DefaultOutputModes: []string{"text/plain"},
		Skills: []Skill{
			{
				ID:          "weather_query",
				Name:        "天气查询",
				Description: "查询指定城市的实时天气信息，包括温度、天气状况、湿度和风力。基于 MCP 风格的工具调用。",
				Tags:        []string{"weather", "tool", "mcp", "eino"},
				Examples:    []string{},
			},
		},
		SupportsAuthExt: false,
	}
}

// ---------- JSON-RPC Types ----------

type JSONRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type JSONRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  interface{}     `json:"result,omitempty"`
	Error   *JSONRPCError   `json:"error,omitempty"`
}

type JSONRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type MessageSendParams struct {
	Message json.RawMessage `json:"message"`
}

type A2AMessage struct {
	MessageID string    `json:"messageId"`
	Role      string    `json:"role"`
	Parts     []A2APart `json:"parts"`
}

type A2APart struct {
	Kind     string                 `json:"kind,omitempty"`
	Type     string                 `json:"type,omitempty"`
	Text     string                 `json:"text,omitempty"`
	Metadata map[string]interface{} `json:"metadata,omitempty"`
}

// A2A v0.3.0 Task response format
type A2ATask struct {
	ID        string         `json:"id"`
	ContextID string         `json:"contextId"`
	Status    A2ATaskStatus  `json:"status"`
	Artifacts []A2AArtifact  `json:"artifacts,omitempty"`
}

type A2ATaskStatus struct {
	State   string      `json:"state"`
	Message *A2AMessage `json:"message,omitempty"`
}

type A2AArtifact struct {
	ArtifactID string    `json:"artifactId"`
	Parts      []A2APart `json:"parts"`
}

// ---------- A2A Server ----------

type Server struct {
	agent adk.Agent
	card  AgentCard
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/.well-known/agent-card.json" && r.Method == http.MethodGet {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(s.card)
		return
	}

	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req JSONRPCRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONRPCError(w, nil, -32700, "Parse error: "+err.Error())
		return
	}

	if req.Method != "message/send" {
		writeJSONRPCError(w, req.ID, -32601, "Method not found: "+req.Method)
		return
	}

	userText := extractUserText(req.Params)
	if userText == "" {
		writeJSONRPCError(w, req.ID, -32602, "Empty message or unparseable params")
		return
	}

	log.Printf("[eino_agent] 收到请求: %s", truncate(userText, 100))

	result, err := s.runAgent(r.Context(), userText)
	if err != nil {
		log.Printf("[eino_agent] agent error: %v", err)
		writeJSONRPCError(w, req.ID, -32000, "Agent error: "+err.Error())
		return
	}

	if result.Status.Message != nil && len(result.Status.Message.Parts) > 0 {
		log.Printf("[eino_agent] 响应: %s", truncate(result.Status.Message.Parts[0].Text, 100))
	}
	writeJSONRPCResult(w, req.ID, result)
}

func (s *Server) runAgent(ctx context.Context, query string) (*A2ATask, error) {
	runner := adk.NewRunner(ctx, adk.RunnerConfig{Agent: s.agent})
	iter := runner.Query(ctx, query)

	var finalText string
	var toolCallInfos []string

	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		if event.Err != nil {
			return nil, event.Err
		}
		if event.Output == nil || event.Output.MessageOutput == nil {
			continue
		}

		mv := event.Output.MessageOutput
		msg, err := consumeMessageVariant(mv)
		if err != nil {
			continue
		}
		if msg == nil {
			continue
		}

		switch msg.Role {
		case schema.Assistant:
			if len(msg.ToolCalls) > 0 {
				for _, tc := range msg.ToolCalls {
					info := fmt.Sprintf("🔧 调用工具 %s(%s)", tc.Function.Name, truncate(tc.Function.Arguments, 100))
					toolCallInfos = append(toolCallInfos, info)
					log.Printf("[eino_agent] %s", info)
				}
			} else if msg.Content != "" {
				finalText = msg.Content
			}
		case schema.Tool:
			info := fmt.Sprintf("↩️ %s 返回: %s", msg.Name, truncate(msg.Content, 200))
			toolCallInfos = append(toolCallInfos, info)
			log.Printf("[eino_agent] %s", info)
		}
	}

	if finalText == "" {
		finalText = "(Agent 未生成有效回复)"
	}

	// Include tool call trace in the response text
	responseText := finalText
	if len(toolCallInfos) > 0 {
		responseText += "\n\n---\n📋 工具调用记录:\n" + strings.Join(toolCallInfos, "\n")
	}

	taskID := fmt.Sprintf("task-%s", generateID())
	contextID := fmt.Sprintf("ctx-%s", generateID())
	msgID := fmt.Sprintf("msg-%s", generateID())

	return &A2ATask{
		ID:        taskID,
		ContextID: contextID,
		Status: A2ATaskStatus{
			State: "completed",
			Message: &A2AMessage{
				MessageID: msgID,
				Role:      "agent",
				Parts:     []A2APart{{Kind: "text", Text: responseText}},
			},
		},
	}, nil
}

func generateID() string {
	return uuid.New().String()[:12]
}

func consumeMessageVariant(mv *adk.MessageVariant) (*schema.Message, error) {
	if mv.IsStreaming && mv.MessageStream != nil {
		var msgs []*schema.Message
		for {
			msg, err := mv.MessageStream.Recv()
			if err == io.EOF {
				break
			}
			if err != nil {
				return nil, err
			}
			msgs = append(msgs, msg)
		}
		if len(msgs) == 0 {
			return nil, nil
		}
		return schema.ConcatMessages(msgs)
	}
	return mv.Message, nil
}

func writeJSONRPCResult(w http.ResponseWriter, id json.RawMessage, result interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(JSONRPCResponse{JSONRPC: "2.0", ID: id, Result: result})
}

func writeJSONRPCError(w http.ResponseWriter, id json.RawMessage, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(JSONRPCResponse{JSONRPC: "2.0", ID: id, Error: &JSONRPCError{Code: code, Message: msg}})
}

// extractUserText flexibly parses A2A params to get the user's text.
// Handles both v0.3 and v1.0 message formats, and arbitrary nested structures.
func extractUserText(raw json.RawMessage) string {
	var params map[string]json.RawMessage
	if err := json.Unmarshal(raw, &params); err != nil {
		return ""
	}
	msgRaw, ok := params["message"]
	if !ok {
		return ""
	}
	var msgMap map[string]json.RawMessage
	if err := json.Unmarshal(msgRaw, &msgMap); err != nil {
		return ""
	}
	partsRaw, ok := msgMap["parts"]
	if !ok {
		return ""
	}
	var parts []map[string]interface{}
	if err := json.Unmarshal(partsRaw, &parts); err != nil {
		return ""
	}
	var texts []string
	for _, p := range parts {
		if t, ok := p["text"].(string); ok && t != "" {
			texts = append(texts, t)
		}
	}
	return strings.Join(texts, " ")
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "…"
}

func main() {
	ctx := context.Background()

	host := envOr("EINO_AGENT_HOST", "0.0.0.0")
	port := envOr("EINO_AGENT_PORT", "8005")
	serviceName := envOr("SERVICE_NAME", "localhost")
	externalURL := envOr("EINO_AGENT_EXTERNAL_URL",
		fmt.Sprintf("http://%s:%s", serviceName, port))
	modelName := envOr("OPENAI_MODEL", "glm-4.5-air")
	apiKey := envOr("OPENAI_API_KEY", "")
	baseURL := envOr("OPENAI_BASE_URL", "")

	chatModel, err := openai.NewChatModel(ctx, &openai.ChatModelConfig{
		Model:   modelName,
		APIKey:  apiKey,
		BaseURL: baseURL,
	})
	if err != nil {
		log.Fatalf("failed to create chat model: %v", err)
	}

	weatherTool, err := utils.InferTool("get_weather",
		"查询指定城市的天气信息（温度、天气状况、湿度、风力）。输入城市名称即可。",
		getWeather)
	if err != nil {
		log.Fatalf("failed to create weather tool: %v", err)
	}

	agent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:        "eino_agent",
		Description: "基于 Eino 框架的 Go Agent，具备天气查询等工具能力。",
		Instruction: `你是一个基于 CloudWeGo Eino 框架的智能助手，运行在 Go 语言环境中。
你具备以下能力：
1. 天气查询：使用 get_weather 工具查询任何城市的天气状况。
2. 通用对话：回答用户的各种问题。

规则：
- 当用户询问天气时，必须调用 get_weather 工具获取数据，不要编造。
- 使用简洁的中文回答。
- 工具调用失败时如实告知。`,
		Model: chatModel,
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{weatherTool},
			},
		},
		MaxIterations: 10,
	})
	if err != nil {
		log.Fatalf("failed to create agent: %v", err)
	}

	srv := &Server{
		agent: agent,
		card:  buildAgentCard(externalURL),
	}

	addr := fmt.Sprintf("%s:%s", host, port)
	log.Printf("[eino_agent] A2A server starting on http://%s", addr)
	log.Printf("[eino_agent] Agent card: http://%s/.well-known/agent-card.json", addr)
	log.Printf("[eino_agent] Model: %s, BaseURL: %s", modelName, baseURL)

	if err := http.ListenAndServe(addr, srv); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
