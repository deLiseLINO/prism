package chat

type chunk struct {
	ID      string        `json:"id"`
	Object  string        `json:"object"`
	Created int64         `json:"created"`
	Model   string        `json:"model"`
	Choices []chunkChoice `json:"choices"`
	Usage   *usage        `json:"usage,omitempty"`
}

type chunkChoice struct {
	Index        int     `json:"index"`
	Delta        delta   `json:"delta"`
	FinishReason *string `json:"finish_reason"`
}

type delta struct {
	Role             string          `json:"role,omitempty"`
	Content          *string         `json:"content,omitempty"`
	ReasoningContent *string         `json:"reasoning_content,omitempty"`
	ToolCalls        []toolCallDelta `json:"tool_calls,omitempty"`
}

type toolCallDelta struct {
	Index    int        `json:"index"`
	ID       *string    `json:"id,omitempty"`
	Type     *string    `json:"type,omitempty"`
	Function *funcDelta `json:"function,omitempty"`
}

type funcDelta struct {
	Name      *string `json:"name,omitempty"`
	Arguments *string `json:"arguments,omitempty"`
}

type usage struct {
	PromptTokens            int64                  `json:"prompt_tokens"`
	CompletionTokens        int64                  `json:"completion_tokens"`
	TotalTokens             int64                  `json:"total_tokens"`
	PromptTokensDetails     promptTokenDetails     `json:"prompt_tokens_details"`
	CompletionTokensDetails completionTokenDetails `json:"completion_tokens_details"`
}

type promptTokenDetails struct {
	CachedTokens int64 `json:"cached_tokens"`
}

type completionTokenDetails struct {
	ReasoningTokens int64 `json:"reasoning_tokens"`
}

type completion struct {
	ID      string   `json:"id"`
	Object  string   `json:"object"`
	Created int64    `json:"created"`
	Model   string   `json:"model"`
	Choices []choice `json:"choices"`
	Usage   usage    `json:"usage"`
}

type choice struct {
	Index        int     `json:"index"`
	Message      message `json:"message"`
	FinishReason string  `json:"finish_reason"`
}

type message struct {
	Role      string         `json:"role"`
	Content   string         `json:"content"`
	ToolCalls []toolCallFull `json:"tool_calls,omitempty"`
}

type toolCallFull struct {
	ID       string   `json:"id"`
	Type     string   `json:"type"`
	Function funcFull `json:"function"`
}

type funcFull struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type errorEnvelope struct {
	Error errorBody `json:"error"`
}

type errorBody struct {
	Message string  `json:"message"`
	Type    string  `json:"type"`
	Param   *string `json:"param"`
	Code    string  `json:"code"`
}
