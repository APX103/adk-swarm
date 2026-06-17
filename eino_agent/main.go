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
// Agent Registry — dynamic endpoint discovery
// =====================================================================

type RegistryAgent struct {
	Name        string `json:"name"`
	URL         string `json:"url"`
	Description string `json:"description"`
	Type        string `json:"type"`
}

type RegistryResponse struct {
	Agents []RegistryAgent `json:"agents"`
}

var registryCache = map[string]string{}
var registryFetched bool

func fetchRegistryAgents(registryURL string) map[string]string {
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(strings.TrimRight(registryURL, "/") + "/agents")
	if err != nil {
		log.Printf("[eino_agent] registry fetch failed: %v", err)
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		log.Printf("[eino_agent] registry returned %d", resp.StatusCode)
		return nil
	}
	var r RegistryResponse
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		log.Printf("[eino_agent] registry decode failed: %v", err)
		return nil
	}
	out := make(map[string]string, len(r.Agents))
	for _, a := range r.Agents {
		if a.Name != "" && a.URL != "" {
			out[a.Name] = a.URL
		}
	}
	log.Printf("[eino_agent] registry loaded %d agents", len(out))
	return out
}

func getAgentURL(name, fallback string) string {
	if !registryFetched {
		if registryURL := os.Getenv("AGENT_REGISTRY_URL"); registryURL != "" {
			if m := fetchRegistryAgents(registryURL); m != nil {
				registryCache = m
			}
		}
		registryFetched = true
	}
	if url, ok := registryCache[name]; ok {
		return url
	}
	return envOr(name+"_URL", fallback)
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
// Delegation Tools — ask_main_agent & ask_comedian
// =====================================================================

type AskMainAgentInput struct {
	Request string `json:"request" jsonschema:"description=要询问主调度Agent(main_agent)的问题，例如获取当前时间。"`
}

type DelegateOutput struct {
	Response string `json:"response"`
}

func askMainAgent(_ context.Context, input *AskMainAgentInput) (*DelegateOutput, error) {
	url := getAgentURL("main_agent", "http://localhost:8081")
	log.Printf("[eino_agent] → 调用 main_agent: %s", truncate(input.Request, 100))
	resp, err := callA2AAgent(url, input.Request)
	if err != nil {
		log.Printf("[eino_agent] ✗ main_agent 调用失败: %v", err)
		return nil, fmt.Errorf("main_agent 调用失败: %w", err)
	}
	log.Printf("[eino_agent] ← main_agent 返回: %s", truncate(resp, 200))
	return &DelegateOutput{Response: resp}, nil
}

type AskComedianInput struct {
	Request string `json:"request" jsonschema:"description=要让笑话Agent(comedian)做的事情，例如讲一个关于某主题的笑话。"`
}

func askComedian(_ context.Context, input *AskComedianInput) (*DelegateOutput, error) {
	url := getAgentURL("comedian_agent", "http://localhost:8003")
	log.Printf("[eino_agent] → 调用 comedian: %s", truncate(input.Request, 100))
	resp, err := callA2AAgent(url, input.Request)
	if err != nil {
		log.Printf("[eino_agent] ✗ comedian 调用失败: %v", err)
		return nil, fmt.Errorf("comedian 调用失败: %w", err)
	}
	log.Printf("[eino_agent] ← comedian 返回: %s", truncate(resp, 200))
	return &DelegateOutput{Response: resp}, nil
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
		Description: "基于 CloudWeGo Eino 框架的 Go Agent。具备天气查询工具，" +
			"并可通过 A2A 协议调度 main_agent（获取时间）和 comedian_agent（讲笑话）。",
		URL:                externalURL,
		Version:            "2.0.0",
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
				ID:          "delegate_time",
				Name:        "获取时间 (via main_agent)",
				Description: "通过 A2A 调用 main_agent 获取当前时间。",
				Tags:        []string{"time", "a2a", "delegation"},
				Examples:    []string{"现在几点了", "当前时间是多少"},
			},
			{
				ID:          "delegate_joke",
				Name:        "讲笑话 (via comedian_agent)",
				Description: "通过 A2A 调用 comedian_agent 讲一个笑话。",
				Tags:        []string{"joke", "humor", "a2a", "delegation"},
				Examples:    []string{"讲个程序员笑话", "来个笑话听听"},
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
  <p>Go + CloudWeGo Eino | A2A 双向调度</p>
  <div class="badges">
    <span class="badge badge-go">Go 1.21</span>
    <span class="badge badge-tool">get_weather</span>
    <span class="badge badge-a2a">→ main_agent (时间)</span>
    <span class="badge badge-a2a">→ comedian (笑话)</span>
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

	mainAgentTool, err := utils.InferTool("ask_main_agent",
		"通过 A2A 协议调用 main_agent（主调度Agent）。main_agent 可以获取当前时间、"+
			"回答通用问题等。当用户询问当前时间或需要 main_agent 的能力时使用。",
		askMainAgent)
	if err != nil {
		log.Fatalf("failed to create ask_main_agent tool: %v", err)
	}

	comedianTool, err := utils.InferTool("ask_comedian",
		"通过 A2A 协议调用 comedian_agent（笑话Agent）。当用户想听笑话、"+
			"需要幽默内容时使用。给它一个主题即可。",
		askComedian)
	if err != nil {
		log.Fatalf("failed to create ask_comedian tool: %v", err)
	}

	agent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:        "eino_agent",
		Description: "基于 CloudWeGo Eino 框架的 Go Agent，具备天气查询、时间获取和讲笑话能力。",
		Instruction: `你是一个基于 CloudWeGo Eino 框架的智能助手，运行在 Go 语言环境中。
你具备以下能力：
1. 天气查询：使用 get_weather 工具查询任何城市的天气状况。
2. 获取时间：使用 ask_main_agent 工具，让 main_agent 返回当前时间。
3. 讲笑话：使用 ask_comedian 工具，让 comedian_agent 讲一个笑话。
4. 通用对话：回答用户的各种问题。

规则：
- 当用户询问天气时，必须调用 get_weather 工具获取数据，不要编造。
- 当用户询问当前时间时，必须调用 ask_main_agent 工具获取，不要编造。
- 当用户想听笑话或幽默内容时，必须调用 ask_comedian 工具获取，不要自己编笑话。
- 使用简洁的中文回答。
- 工具调用失败时如实告知用户。`,
		Model: chatModel,
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{weatherTool, mainAgentTool, comedianTool},
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
	log.Printf("[eino_agent] Model: %s | main_agent: %s | comedian: %s",
		modelName,
		envOr("MAIN_AGENT_A2A_URL", "http://localhost:8081"),
		envOr("COMEDIAN_AGENT_URL", "http://localhost:8003"))

	if err := http.ListenAndServe(addr, srv); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
