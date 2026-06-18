package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
)

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// =====================================================================
// Agent Registry — MCP client: self-registration + discovery
//
// The registry is exposed as an MCP server (SSE at <url>/sse) with two tools:
//   - register_agent(name, url, description, type)
//   - list_agents()  -> {agents:[{name,url,description,type}, ...]} (healthy only)
//
// We connect via MCP (not REST) so this same pattern works for any MCP-capable
// agent in any language: connect the SSE endpoint, get register+discover tools.
// Actual agent-to-agent calls still go P2P over A2A (message/send) — the
// registry is a phonebook, not a relay.
// =====================================================================

// RegistryClient wraps an MCP SSE connection to the agent registry.
type RegistryClient struct {
	sseURL   string
	cli      *mcpclient.Client
	callerID string
	callerKey string
}

// AgentEntry is one agent in the registry's list_agents result.
type AgentEntry struct {
	Name        string `json:"name"`
	URL         string `json:"url"`
	Description string `json:"description"`
	Type        string `json:"type"`
}

// NewRegistryClient connects to the registry's MCP SSE endpoint and initializes
// the session. Returns error if the registry is unreachable; callers should
// treat this as non-fatal (the agent can still run, just without discovery).
func NewRegistryClient(ctx context.Context, registryURL, callerID, callerKey string) (*RegistryClient, error) {
	sseURL := strings.TrimRight(registryURL, "/") + "/sse"
	opts := []transport.ClientOption{}
	if callerKey != "" {
		opts = append(opts, mcpclient.WithHeaders(map[string]string{
			"X-Registry-Key": callerKey,
		}))
	}
	cli, err := mcpclient.NewSSEMCPClient(sseURL, opts...)
	if err != nil {
		return nil, fmt.Errorf("create SSE client: %w", err)
	}
	// Must Start before Initialize for SSE transport.
	if err := cli.Start(ctx); err != nil {
		return nil, fmt.Errorf("start SSE client: %w", err)
	}
	initReq := mcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcp.Implementation{Name: "eino_agent", Version: "2.1.0"}
	if _, err := cli.Initialize(ctx, initReq); err != nil {
		return nil, fmt.Errorf("initialize MCP session: %w", err)
	}
	log.Printf("[eino_agent] connected to registry MCP at %s", sseURL)
	return &RegistryClient{sseURL: sseURL, cli: cli, callerID: callerID, callerKey: callerKey}, nil
}

// Close releases the MCP session.
func (rc *RegistryClient) Close() error {
	if rc.cli == nil {
		return nil
	}
	return rc.cli.Close()
}

// RegisterSelf registers this agent into the registry. Non-fatal on failure.
func (rc *RegistryClient) RegisterSelf(ctx context.Context, name, url, description, agentType string) {
	req := mcp.CallToolRequest{}
	req.Params.Name = "register_agent"
	req.Params.Arguments = map[string]any{
		"caller_id":    rc.callerID,
		"caller_key":   rc.callerKey,
		"name":         name,
		"url":          url,
		"description":  description,
		"type":         agentType,
	}
	_, err := rc.cli.CallTool(ctx, req)
	if err != nil {
		log.Printf("[eino_agent] self-registration failed (non-fatal): %v", err)
		return
	}
	log.Printf("[eino_agent] registered self as %q @ %s", name, url)
}

// ListAgents returns the currently-reachable agents (excluding self).
func (rc *RegistryClient) ListAgents(ctx context.Context, selfName string) []AgentEntry {
	req := mcp.CallToolRequest{}
	req.Params.Name = "list_agents"
	req.Params.Arguments = map[string]any{
		"caller_id":  rc.callerID,
		"caller_key": rc.callerKey,
	}
	res, err := rc.cli.CallTool(ctx, req)
	if err != nil {
		log.Printf("[eino_agent] list_agents failed: %v", err)
		return nil
	}
	// MCP tool returns content[].text holding JSON: {"agents":[...]}
	var out []AgentEntry
	for _, c := range res.Content {
		if tc, ok := c.(mcp.TextContent); ok && tc.Text != "" {
			var payload struct {
				Agents []AgentEntry `json:"agents"`
			}
			if err := json.Unmarshal([]byte(tc.Text), &payload); err == nil {
				out = append(out, payload.Agents...)
			}
		}
	}
	// Filter out self.
	filtered := out[:0]
	for _, a := range out {
		if a.Name != selfName {
			filtered = append(filtered, a)
		}
	}
	return filtered
}

// =====================================================================
// Weather Tool
// =====================================================================

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

// =====================================================================
// A2A Client — call remote agents via JSON-RPC
// =====================================================================

var a2aClient = &http.Client{Timeout: 120 * time.Second}

func callA2AAgent(baseURL, message string) (string, error) {
	payload := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      uuid.New().String(),
		"method":  "message/send",
		"params": map[string]interface{}{
			"message": map[string]interface{}{
				"messageId": uuid.New().String(),
				"role":      "user",
				"parts": []map[string]interface{}{
					{"kind": "text", "text": message},
				},
			},
		},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal error: %w", err)
	}

	endpoints := []string{
		strings.TrimRight(baseURL, "/") + "/",
		strings.TrimRight(baseURL, "/") + "/jsonrpc",
	}

	var lastErr error
	for _, ep := range endpoints {
		text, err := doA2APost(ep, body)
		if err != nil {
			lastErr = err
			continue
		}
		return text, nil
	}
	return "", fmt.Errorf("failed to call A2A agent at %s: %v", baseURL, lastErr)
}

func doA2APost(url string, body []byte) (string, error) {
	resp, err := a2aClient.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var rpcResp map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		return "", fmt.Errorf("decode error: %w", err)
	}
	if _, ok := rpcResp["result"]; ok {
		return extractA2AText(rpcResp["result"]), nil
	}
	if errObj, ok := rpcResp["error"]; ok {
		return "", fmt.Errorf("RPC error: %v", errObj)
	}
	return "", fmt.Errorf("non-JSONRPC response")
}

func extractA2AText(result interface{}) string {
	m, ok := result.(map[string]interface{})
	if !ok {
		return fmt.Sprintf("%v", result)
	}
	var texts []string
	// status.message.parts (Task format from Go/Python A2A)
	if st, ok := m["status"].(map[string]interface{}); ok {
		if msg, ok := st["message"].(map[string]interface{}); ok {
			texts = append(texts, partsText(msg)...)
		}
	}
	// artifacts[].parts (Python ADK A2A)
	if arts, ok := m["artifacts"].([]interface{}); ok {
		for _, a := range arts {
			if am, ok := a.(map[string]interface{}); ok {
				texts = append(texts, partsText(am)...)
			}
		}
	}
	// direct message.parts
	if msg, ok := m["message"].(map[string]interface{}); ok {
		texts = append(texts, partsText(msg)...)
	}
	// direct parts
	texts = append(texts, partsText(m)...)

	if len(texts) == 0 {
		b, _ := json.Marshal(result)
		return string(b)
	}
	return strings.Join(texts, "\n")
}

func partsText(m map[string]interface{}) []string {
	parts, ok := m["parts"].([]interface{})
	if !ok {
		return nil
	}
	var out []string
	for _, p := range parts {
		if pm, ok := p.(map[string]interface{}); ok {
			if t, ok := pm["text"].(string); ok && t != "" {
				out = append(out, t)
			}
		}
	}
	return out
}

// =====================================================================
// Delegation Tools — dynamically built from registry discovery
//
// For each agent discovered via list_agents(), we build a closure-backed tool
// that calls it over A2A (message/send). This is P2P: the registry only told
// us the URL; we contact the peer directly.
// =====================================================================

type DelegateInput struct {
	Request string `json:"request" jsonschema:"description=要发送给该 Agent 的请求内容。"`
}

type DelegateOutput struct {
	Response string `json:"response"`
}

// EmptyInput is the zero-arg input type for tools that take no arguments
// (InferTool's generic inference needs a concrete input type).
type EmptyInput struct{}

// CallAgentInput is the input for the runtime call_agent tool.
type CallAgentInput struct {
	Name    string `json:"name" jsonschema:"description=要调用的 Agent 名称（必须先在集群中存在）。"`
	Request string `json:"request" jsonschema:"description=要发送给该 Agent 的请求内容。"`
}

// buildCallAgentTool builds a tool that resolves an agent by name from the
// registry at call time and contacts it over A2A. This closes the discovery
// loop for peers that registered after our startup snapshot (or for any name
// the LLM learned from list_registry_agents).
func buildCallAgentTool(rc *RegistryClient, selfName string) tool.BaseTool {
	fn := func(ctx context.Context, input *CallAgentInput) (*DelegateOutput, error) {
		// Resolve the peer's URL at call time (live discovery).
		peers := rc.ListAgents(ctx, selfName)
		var found *AgentEntry
		for i := range peers {
			if peers[i].Name == input.Name {
				found = &peers[i]
				break
			}
		}
		if found == nil {
			return nil, fmt.Errorf("agent %q 不在集群中（或当前不可达）", input.Name)
		}
		log.Printf("[eino_agent] → 调用 %s @ %s: %s", found.Name, found.URL, truncate(input.Request, 100))
		resp, err := callA2AAgent(found.URL, input.Request)
		if err != nil {
			log.Printf("[eino_agent] ✗ %s 调用失败: %v", found.Name, err)
			return nil, fmt.Errorf("%s 调用失败: %w", found.Name, err)
		}
		log.Printf("[eino_agent] ← %s 返回: %s", found.Name, truncate(resp, 200))
		return &DelegateOutput{Response: resp}, nil
	}
	t, err := utils.InferTool("call_agent",
		"按名称调用集群中的任意 Agent（A2A）。先用 list_registry_agents 确认该 Agent 存在，"+
			"再用本工具传入 name 和 request。例如要获取当前时间，name 填 main_agent。",
		fn)
	if err != nil {
		log.Printf("[eino_agent] failed to build call_agent tool: %v", err)
		return nil
	}
	return t
}

// buildDelegateTools turns discovered agents into A2A delegate tools.
func buildDelegateTools(peers []AgentEntry) []tool.BaseTool {
	var tools []tool.BaseTool
	for _, p := range peers {
		peer := p // capture
		fn := func(_ context.Context, input *DelegateInput) (*DelegateOutput, error) {
			log.Printf("[eino_agent] → 调用 %s: %s", peer.Name, truncate(input.Request, 100))
			resp, err := callA2AAgent(peer.URL, input.Request)
			if err != nil {
				log.Printf("[eino_agent] ✗ %s 调用失败: %v", peer.Name, err)
				return nil, fmt.Errorf("%s 调用失败: %w", peer.Name, err)
			}
			log.Printf("[eino_agent] ← %s 返回: %s", peer.Name, truncate(resp, 200))
			return &DelegateOutput{Response: resp}, nil
		}
		// Tool name derived from agent name; description guides the LLM to route.
		toolName := "ask_" + sanitizeToolName(peer.Name)
		desc := peer.Description
		if desc == "" {
			desc = "通过 A2A 调用 " + peer.Name + " agent。"
		}
		t, err := utils.InferTool(toolName, desc, fn)
		if err != nil {
			log.Printf("[eino_agent] skip delegate tool for %s: %v", peer.Name, err)
			continue
		}
		tools = append(tools, t)
	}
	return tools
}

func sanitizeToolName(s string) string {
	out := make([]byte, 0, len(s))
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' {
			out = append(out, byte(r))
		} else {
			out = append(out, '_')
		}
	}
	return string(out)
}

// delegateToolsHelp renders the discovered peers for the agent instruction.
func delegateToolsHelp(peers []AgentEntry) string {
	if len(peers) == 0 {
		return "（当前集群没有发现其他 Agent）"
	}
	var lines []string
	for _, p := range peers {
		desc := p.Description
		if desc == "" {
			desc = "（无描述）"
		}
		lines = append(lines, fmt.Sprintf("- ask_%s: %s", sanitizeToolName(p.Name), desc))
	}
	return strings.Join(lines, "\n")
}

// mustBuildListAgentsTool exposes a "list_registry_agents" LLM tool that
// re-queries the registry at runtime (in case peers changed since startup).
func mustBuildListAgentsTool(rc *RegistryClient, selfName string) tool.BaseTool {
	type agentsResult struct {
		Agents []AgentEntry `json:"agents"`
	}
	fn := func(ctx context.Context, _ *EmptyInput) (*agentsResult, error) {
		peers := rc.ListAgents(ctx, selfName)
		return &agentsResult{Agents: peers}, nil
	}
	t, err := utils.InferTool("list_registry_agents",
		"重新发现集群中所有当前可用的 Agent。返回每个 Agent 的 name/url/description。"+
			"当用户问'有哪些 agent'或你想确认能否调用某 agent 时使用。无需参数。",
		fn)
	if err != nil {
		log.Printf("[eino_agent] failed to build list_registry_agents tool: %v", err)
		return nil
	}
	return t
}

// =====================================================================
// Agent Card (A2A v0.3.0)
// =====================================================================

type AgentCard struct {
	Name               string        `json:"name"`
	Description        string        `json:"description"`
	URL                string        `json:"url"`
	Version            string        `json:"version"`
	ProtocolVersion    string        `json:"protocolVersion"`
	PreferredTransport string        `json:"preferredTransport"`
	Capabilities       *Capabilities `json:"capabilities"`
	DefaultInputModes  []string      `json:"defaultInputModes"`
	DefaultOutputModes []string      `json:"defaultOutputModes"`
	Skills             []Skill       `json:"skills"`
	SupportsAuthExt    bool          `json:"supportsAuthenticatedExtendedCard"`
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
		Name: "eino_agent",
		Description: "基于 CloudWeGo Eino 框架的 Go Agent。具备天气查询、报时工具，" +
			"并通过 Agent Registry（MCP 服务发现）接入集群，可调用集群中其他 Agent。",
		URL:                externalURL,
		Version:            "2.1.0",
		ProtocolVersion:    "0.3.0",
		PreferredTransport: "JSONRPC",
		Capabilities:       &Capabilities{},
		DefaultInputModes:  []string{"text/plain"},
		DefaultOutputModes: []string{"text/plain"},
		Skills: []Skill{
			{
				ID:          "weather_query",
				Name:        "天气查询",
				Description: "使用内置 get_weather 工具查询城市天气。",
				Tags:        []string{"weather", "tool"},
				Examples:    []string{"查询北京天气", "上海今天天气怎么样"},
			},
			{
				ID:          "cluster_discovery",
				Name:        "集群服务发现",
				Description: "通过 Agent Registry 发现并调用集群中的其他 Agent（A2A）。例如报时能力由集群中的 main_agent 提供。",
				Tags:        []string{"discovery", "a2a", "mcp"},
				Examples:    []string{"集群里有哪些 agent", "现在几点了", "让笑话 agent 讲个笑话"},
			},
		},
		SupportsAuthExt: false,
	}
}

// =====================================================================
// JSON-RPC Types
// =====================================================================

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

type A2ATask struct {
	ID        string        `json:"id"`
	ContextID string        `json:"contextId"`
	Status    A2ATaskStatus `json:"status"`
	Artifacts []A2AArtifact `json:"artifacts,omitempty"`
}

type A2ATaskStatus struct {
	State   string      `json:"state"`
	Message *A2AMessage `json:"message,omitempty"`
}

type A2AArtifact struct {
	ArtifactID string    `json:"artifactId"`
	Parts      []A2APart `json:"parts"`
}

// =====================================================================
// Agent Result (shared between A2A and Chat API)
// =====================================================================

type AgentResult struct {
	Response  string   `json:"response"`
	ToolCalls []string `json:"tool_calls,omitempty"`
	Error     string   `json:"error,omitempty"`
}

// =====================================================================
// A2A + Chat Server
// =====================================================================

type Server struct {
	agent adk.Agent
	card  AgentCard
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/.well-known/agent-card.json" && r.Method == http.MethodGet:
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(s.card)

	case r.URL.Path == "/ui" && r.Method == http.MethodGet:
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(devUIHTML))

	case r.URL.Path == "/api/chat" && r.Method == http.MethodPost:
		s.handleChatAPI(w, r)

	case r.Method == http.MethodPost && (r.URL.Path == "/" || r.URL.Path == ""):
		s.handleA2A(w, r)

	default:
		if r.URL.Path == "/" && r.Method == http.MethodGet {
			http.Redirect(w, r, "/ui", http.StatusTemporaryRedirect)
			return
		}
		http.NotFound(w, r)
	}
}

func (s *Server) handleA2A(w http.ResponseWriter, r *http.Request) {
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
		writeJSONRPCError(w, req.ID, -32602, "Empty message")
		return
	}

	log.Printf("[eino_agent] A2A 收到: %s", truncate(userText, 100))

	result := s.runAgent(r.Context(), userText)
	if result.Error != "" {
		writeJSONRPCError(w, req.ID, -32000, result.Error)
		return
	}

	responseText := result.Response
	if len(result.ToolCalls) > 0 {
		responseText += "\n\n---\n📋 工具调用记录:\n" + strings.Join(result.ToolCalls, "\n")
	}

	task := &A2ATask{
		ID:        "task-" + generateID(),
		ContextID: "ctx-" + generateID(),
		Status: A2ATaskStatus{
			State: "completed",
			Message: &A2AMessage{
				MessageID: "msg-" + generateID(),
				Role:      "agent",
				Parts:     []A2APart{{Kind: "text", Text: responseText}},
			},
		},
	}
	writeJSONRPCResult(w, req.ID, task)
}

func (s *Server) handleChatAPI(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Message string `json:"message"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Message == "" {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(AgentResult{Error: "empty or invalid message"})
		return
	}

	log.Printf("[eino_agent] Chat API: %s", truncate(body.Message, 100))
	result := s.runAgent(r.Context(), body.Message)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func (s *Server) runAgent(ctx context.Context, query string) AgentResult {
	runner := adk.NewRunner(ctx, adk.RunnerConfig{Agent: s.agent})
	iter := runner.Query(ctx, query)

	var finalText string
	var toolCalls []string

	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		if event.Err != nil {
			return AgentResult{Error: event.Err.Error()}
		}
		if event.Output == nil || event.Output.MessageOutput == nil {
			continue
		}

		msg, err := consumeMessageVariant(event.Output.MessageOutput)
		if err != nil || msg == nil {
			continue
		}

		switch msg.Role {
		case schema.Assistant:
			if len(msg.ToolCalls) > 0 {
				for _, tc := range msg.ToolCalls {
					info := fmt.Sprintf("🔧 %s(%s)", tc.Function.Name, truncate(tc.Function.Arguments, 150))
					toolCalls = append(toolCalls, info)
					log.Printf("[eino_agent] %s", info)
				}
			} else if msg.Content != "" {
				finalText = msg.Content
			}
		case schema.Tool:
			info := fmt.Sprintf("↩️ %s → %s", msg.Name, truncate(msg.Content, 250))
			toolCalls = append(toolCalls, info)
			log.Printf("[eino_agent] %s", info)
		}
	}

	if finalText == "" {
		finalText = "(Agent 未生成有效回复)"
	}
	return AgentResult{Response: finalText, ToolCalls: toolCalls}
}

// =====================================================================
// Helpers
// =====================================================================

func generateID() string { return uuid.New().String()[:12] }

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

// =====================================================================
// Dev Web UI (embedded HTML)
// =====================================================================

const devUIHTML = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Eino Agent - Dev UI</title>
<style>
*{margin:0;padding:0;box-sizing:border-box}
body{background:#0f172a;color:#e2e8f0;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,sans-serif;height:100vh;display:flex;flex-direction:column}
.header{padding:16px 24px;border-bottom:1px solid #1e293b;background:#0f172a;flex-shrink:0}
.header h1{font-size:20px;color:#f1f5f9}
.header p{font-size:12px;color:#64748b;margin-top:4px}
.badges{display:flex;gap:6px;margin-top:8px;flex-wrap:wrap}
.badge{font-size:10px;padding:2px 8px;border-radius:4px;font-weight:500}
.badge-go{background:#0e4f3b;color:#34d399}
.badge-tool{background:#7c2d12;color:#fb923c}
.badge-a2a{background:#3b0f3b;color:#f472b6}
.messages{flex:1;overflow-y:auto;padding:16px 24px;display:flex;flex-direction:column;gap:12px}
.msg{max-width:85%;animation:fadeIn .2s}
@keyframes fadeIn{from{opacity:0;transform:translateY(8px)}to{opacity:1;transform:none}}
.msg-user{align-self:flex-end}
.msg-agent{align-self:flex-start}
.bubble{padding:12px 16px;border-radius:12px;font-size:14px;line-height:1.6;white-space:pre-wrap;word-break:break-word}
.msg-user .bubble{background:#3b82f6;color:#fff;border-bottom-right-radius:4px}
.msg-agent .bubble{background:#1e293b;border:1px solid #334155;border-bottom-left-radius:4px}
.tool-calls{margin-top:6px;display:flex;flex-direction:column;gap:4px}
.tc{background:#0f172a;border-left:3px solid #f97316;padding:6px 10px;font-size:12px;font-family:'SF Mono',Menlo,monospace;color:#94a3b8;border-radius:0 6px 6px 0;line-height:1.5;white-space:pre-wrap;word-break:break-all}
.loading-dot{display:inline-block;width:6px;height:6px;border-radius:50%;background:#64748b;animation:blink 1.2s infinite both}
.loading-dot:nth-child(2){animation-delay:.2s}
.loading-dot:nth-child(3){animation-delay:.4s}
@keyframes blink{0%,80%,100%{opacity:.2}40%{opacity:1}}
.input-area{padding:12px 24px;border-top:1px solid #1e293b;display:flex;gap:8px;flex-shrink:0;background:#0f172a}
.input-area input{flex:1;background:#1e293b;border:1px solid #334155;color:#e2e8f0;padding:12px 16px;border-radius:10px;font-size:14px;outline:none;transition:border .2s}
.input-area input:focus{border-color:#3b82f6}
.input-area button{background:#3b82f6;color:#fff;border:none;padding:12px 24px;border-radius:10px;font-size:14px;cursor:pointer;font-weight:600;transition:background .2s}
.input-area button:hover{background:#2563eb}
.input-area button:disabled{background:#334155;cursor:not-allowed}
</style>
</head>
<body>
<div class="header">
  <h1>🌤️ Eino Agent — Dev UI</h1>
  <p>Go + CloudWeGo Eino | MCP 服务发现 + A2A P2P</p>
  <div class="badges">
    <span class="badge badge-go">Go 1.21</span>
    <span class="badge badge-tool">get_weather</span>
    <span class="badge badge-a2a">MCP → Registry</span>
    <span class="badge badge-a2a">A2A P2P</span>
  </div>
</div>
<div class="messages" id="msgs"></div>
<div class="input-area">
  <input id="inp" type="text" placeholder="试试: 查询北京天气 / 现在几点了 / 讲个程序员笑话" autocomplete="off"/>
  <button id="btn" onclick="send()">发送</button>
</div>
<script>
const msgs=document.getElementById('msgs'),inp=document.getElementById('inp'),btn=document.getElementById('btn');
function esc(s){return s.replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;')}
function addMsg(role,html){
  const d=document.createElement('div');d.className='msg msg-'+role;d.innerHTML=html;
  msgs.appendChild(d);msgs.scrollTop=msgs.scrollHeight;return d;
}
async function send(){
  const text=inp.value.trim();if(!text)return;
  inp.value='';btn.disabled=true;
  addMsg('user','<div class="bubble">'+esc(text)+'</div>');
  const ld=addMsg('agent','<div class="bubble"><span class="loading-dot"></span><span class="loading-dot"></span><span class="loading-dot"></span></div>');
  try{
    const r=await fetch('/api/chat',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({message:text})});
    const d=await r.json();msgs.removeChild(ld);
    let html='<div class="bubble">'+esc(d.response||d.error||'(无响应)')+'</div>';
    if(d.tool_calls&&d.tool_calls.length){
      html+='<div class="tool-calls">'+d.tool_calls.map(t=>'<div class="tc">'+esc(t)+'</div>').join('')+'</div>';
    }
    addMsg('agent',html);
  }catch(e){msgs.removeChild(ld);addMsg('agent','<div class="bubble" style="color:#ef4444">错误: '+esc(e.message)+'</div>');}
  btn.disabled=false;inp.focus();
}
inp.addEventListener('keydown',e=>{if(e.key==='Enter'&&!btn.disabled)send()});
inp.focus();
</script>
</body>
</html>`

// =====================================================================
// Main
// =====================================================================

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
	registryURL := envOr("AGENT_REGISTRY_URL", "")
	selfName := "eino_agent"

	chatModel, err := openai.NewChatModel(ctx, &openai.ChatModelConfig{
		Model:   modelName,
		APIKey:  apiKey,
		BaseURL: baseURL,
	})
	if err != nil {
		log.Fatalf("failed to create chat model: %v", err)
	}

	// Built-in tool: weather (eino's own mock capability).
	// NOTE: no built-in time tool — eino must discover an agent that can tell
	// time (e.g. main_agent) via the registry and call it over A2A.
	weatherTool, err := utils.InferTool("get_weather",
		"查询指定城市的天气信息（温度、天气状况、湿度、风力）。输入城市名称即可。",
		getWeather)
	if err != nil {
		log.Fatalf("failed to create weather tool: %v", err)
	}

	// Connect to the registry via MCP (self-register + discover peers).
	var registry *RegistryClient
	var peers []AgentEntry
	callerKey := envOr("REGISTRY_CLIENT_KEY", "")
	if registryURL != "" {
		rc, rerr := NewRegistryClient(ctx, registryURL, selfName, callerKey)
		if rerr != nil {
			log.Printf("[eino_agent] registry MCP connect failed (non-fatal, running standalone): %v", rerr)
		} else {
			registry = rc
			defer rc.Close()
			// Register self so others can discover us.
			rc.RegisterSelf(ctx, selfName, externalURL,
				"基于 CloudWeGo Eino 的 Go Agent。具备天气查询、报时工具，并能通过 A2A 调用集群中其他 Agent。",
				"orchestrator")
			// Discover peers (snapshot at startup; list_agents MCP tool is also
			// available to the LLM at runtime for re-discovery).
			peers = rc.ListAgents(ctx, selfName)
			log.Printf("[eino_agent] discovered %d peers via registry", len(peers))
			for _, p := range peers {
				log.Printf("[eino_agent]   - %s: %s", p.Name, p.URL)
			}
		}
	} else {
		log.Printf("[eino_agent] AGENT_REGISTRY_URL not set — running standalone (no discovery)")
	}

	// Build dynamic A2A delegate tools from discovered peers.
	delegateTools := buildDelegateTools(peers)

	// Also expose the registry's list_agents as an LLM tool (re-discovery).
	var allTools []tool.BaseTool = []tool.BaseTool{weatherTool}
	allTools = append(allTools, delegateTools...)
	if registry != nil {
		// list_registry_agents: runtime re-discovery of what's in the cluster.
		allTools = append(allTools, mustBuildListAgentsTool(registry, selfName))
		// call_agent: resolve+call any agent by name at runtime (closes the
		// discovery loop for peers that registered after our startup snapshot).
		if ct := buildCallAgentTool(registry, selfName); ct != nil {
			allTools = append(allTools, ct)
		}
	}

	helpText := delegateToolsHelp(peers)

	agent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:        selfName,
		Description: "基于 CloudWeGo Eino 框架的 Go Agent，具备天气查询能力，并通过服务发现调用集群中其他 Agent（如报时）。",
		Instruction: `你是一个基于 CloudWeGo Eino 框架的智能助手，运行在 Go 语言环境中。
你通过 Agent Registry（MCP 服务发现）接入集群。你【自身没有】报时、写笑话、写代码等
能力——这些都必须通过服务发现找到对应的 Agent，再调用它（A2A P2P）。

你的内置能力仅限：
- 天气查询：使用 get_weather 工具。

启动时已发现的集群 Agent（快照，可能不全）：
` + helpText + `

如何调用集群中的其他 Agent（重要）：
1. 如果不确定集群里有什么，先调用 list_registry_agents 重新发现当前可用的 Agent。
2. 用 call_agent 工具调用某个 Agent：传入它的 name 和 request。

典型场景：
- 用户问"现在几点/当前时间"：集群中的 main_agent 会报时。调用 call_agent，
  name="main_agent"，request="现在几点了"。
- 用户要"讲个笑话"：调用 call_agent，name="comedian_agent"（先用 list_registry_agents 确认存在）。

规则：
- 绝不自己猜测或编造时间、笑话等你没有的能力——必须调用集群里的 Agent。
- 当用户询问天气时，调用内置 get_weather 工具。
- 使用简洁的中文回答。工具调用失败时如实告知用户。`,
		Model: chatModel,
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: allTools,
			},
		},
		MaxIterations: 10,
	})
	if err != nil {
		log.Fatalf("failed to create agent: %v", err)
	}

	srv := &Server{agent: agent, card: buildAgentCard(externalURL)}

	addr := fmt.Sprintf("%s:%s", host, port)
	log.Printf("[eino_agent] A2A + Dev UI server on http://%s", addr)
	log.Printf("[eino_agent] Agent card: http://%s/.well-known/agent-card.json", addr)
	log.Printf("[eino_agent] Dev UI: http://%s/ui", addr)
	log.Printf("[eino_agent] Model: %s | registry: %s | peers: %d",
		modelName, registryURL, len(peers))

	if err := http.ListenAndServe(addr, srv); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
