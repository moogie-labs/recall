// Package desktop adapts the shared application to a narrow desktop boundary.
package desktop

type Problem struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Path    string `json:"path"`
}
type OperationReply struct {
	Problem *Problem `json:"problem"`
}
