package antigravity

import "strings"

const (
	IDEVersion = "2.5.5"

	ideClientName = "aidev_client"
	ideOSType     = "windows"
	ideArch       = "amd64"
	ideAuthMethod = "oauth"

	EnvelopeUserAgent = "antigravity"
	RequestType       = "agent"

	DefaultBaseURL = "https://cloudcode-pa.googleapis.com"

	streamMethod = "streamGenerateContent"
	streamQuery  = "?alt=sse"
	apiVersion   = "v1internal"

	requestIDPrefix = "agent-"

	continueNudge = "(continue)"

	emptyPlaceholder           = "(empty)"
	emptyToolOutputPlaceholder = "(empty tool output)"
	missingToolResultText      = "[missing tool_result for this tool_use in history]"
	orphantToolResultPrefix    = "[tool_result without adjacent tool_use: "
	unrepresentableCallPrefix  = "[tool_use without a usable id: "

	signatureStore = "antigravity"
)

func RequestUserAgent() string {
	return "antigravity/ide/" + IDEVersion +
		" (os_type=" + ideOSType +
		"; arch=" + ideArch +
		"; " + ideClientName +
		"; auth_method=" + ideAuthMethod + ")"
}

func StreamURL(baseURL string) string {
	return strings.TrimRight(baseURL, "/") + "/" + apiVersion + ":" + streamMethod + streamQuery
}
