// Package message gives text that the server produces for people a stable
// code and parameters, so that clients can show it in their own language.
// The English text stays alongside for the CLI, logs and clients that do not
// know a code.
package message

// Message is one translatable sentence.
type Message struct {
	// Code names the sentence; it never changes meaning once published.
	// It is empty for free-form text, which clients show as it is.
	Code string `json:"code"`
	// Params are the values the sentence mentions, by name.
	Params map[string]string `json:"params"`
	// Message is the English text.
	Message string `json:"message"`
}

// New returns a message with code and English text; kv are parameter names
// and values in pairs.
func New(code, text string, kv ...string) Message {
	params := make(map[string]string, len(kv)/2)
	for i := 0; i+1 < len(kv); i += 2 {
		params[kv[i]] = kv[i+1]
	}
	return Message{Code: code, Params: params, Message: text}
}

// Text returns a message without a code, for free-form text whose parts are
// not known: clients show the English text.
func Text(text string) Message {
	return Message{Params: map[string]string{}, Message: text}
}

// Texts returns the English text of each message.
func Texts(ms []Message) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.Message
	}
	return out
}
