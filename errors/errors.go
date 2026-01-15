package errors

import "fmt"

// 所有错误，由这个接口包装，以便以msgp序列化，然后传递
type Error struct {
	Code    uint16 `msg:"c"`          // 错误码 (用于程序逻辑判断，如 1001=UserNotFound)
	Msg     string `msg:"m"`          // 错误信息 (用于日志和人类阅读)
	Data    []byte `msg:"d,allownil"` // (可选) 附加数据，例如具体的校验失败字段
	TraceID string `msg:"t"`          // (可选) 分布式追踪 ID
}

func (e *Error) Error() string {
	if e.Code == ClientStandardError || e.Code == ServerStandardError || e.Code == RemoteStandardError {
		return e.Msg
	}
	return fmt.Sprintf("code=%d msg=%s", e.Code, e.Msg)
}

func (e *Error) WithData(data []byte) *Error {
	e.Data = data
	return e
}

func (e *Error) WithTraceID(traceID string) *Error {
	e.TraceID = traceID
	return e
}

func New(code T, msg string) *Error {
	return &Error{Code: code, Msg: msg}
}

func Newf(code T, msg string, args ...any) *Error {
	return &Error{Code: code, Msg: fmt.Sprintf(msg, args...)}
}
