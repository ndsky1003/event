package errors

type T = uint16

// 是一个假错误,不是真的错误,既没有返回值,也没有错误,需要,就是作为特殊功能的返回.eg:重置客户端的EOS
// 如果表示为空的返回值，就需要一个接受的值，但是没有内定的接受。Error是固定的，所以用这个不是错误的错误表示没有返回值
const None T = 0

// client
const (
	ClientInternal         T = 400 //参数错误
	ClientUnauthorized     T = 401 //未授权
	ClientInvalidArgs      T = 402 //参数错误
	CodeClientForbidden    T = 403 //禁止访问
	ClientNotFound         T = 404 //找不到
	ClientMethodNotAllowed T = 405 //方法禁用
	ClientCallError        T = 406 //方法禁用
	ClientReturnInvalid    T = 407 //返回类型错误
	ClientTooManyRequests  T = 429 //请求过多
	ClientSendChanExhaust  T = 497 //请求队列已经耗尽
	ClientCanceled         T = 498 //请求取消
	ClientStandardError    T = 499 //客户端标准错误
)

// server
const (
	ServerInternal           T = 500 //服务器内部错误
	ServerServiceUnavailable T = 503 //服务不可用
	ServerDeadlineExceeded   T = 504 //网关超时
	ServerServiceNotFound    T = 505 //服务未找到
	ServerStandardError      T = 599 //服务器标准错误
)

// remote
const (
	RemoteInternal      T = 600 //远程内部错误
	RemoteTimeout       T = 601 //远程超时错误
	RemoteStandardError T = 699 //远程标准错误
)
