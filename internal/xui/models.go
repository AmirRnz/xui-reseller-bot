package xui

type LoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type APIResponse struct {
	Success bool   `json:"success"`
	Msg     string `json:"msg"`
	Obj     any    `json:"obj"`
}

type Inbound struct {
	ID             int          `json:"id"`
	Up             int64        `json:"up,omitempty"`
	Down           int64        `json:"down,omitempty"`
	Total          int64        `json:"total,omitempty"`
	Remark         string       `json:"remark"`
	Enable         bool         `json:"enable,omitempty"`
	ExpiryTime     int64        `json:"expiryTime,omitempty"`
	ClientStats    []ClientStat `json:"clientStats,omitempty"`
	Listen         string       `json:"listen,omitempty"`
	Port           int          `json:"port"`
	Protocol       string       `json:"protocol"`
	Settings       string       `json:"settings,omitempty"`
	StreamSettings string       `json:"streamSettings,omitempty"`
	Tag            string       `json:"tag"`
	Sniffing       string       `json:"sniffing,omitempty"`
	SSMethod       string       `json:"ssMethod,omitempty"`
	TLSFlowCapable bool         `json:"tlsFlowCapable,omitempty"`
}

type ClientStat struct {
	ID         int    `json:"id"`
	InboundID  int    `json:"inboundId"`
	Enable     bool   `json:"enable"`
	Email      string `json:"email"`
	Up         int64  `json:"up"`
	Down       int64  `json:"down"`
	ExpiryTime int64  `json:"expiryTime"`
	Total      int64  `json:"total"`
	Reset      int    `json:"reset"`
}

type ClientConfig struct {
	ID         string `json:"id,omitempty"`
	Email      string `json:"email"`
	Enable     bool   `json:"enable"`
	ExpiryTime int64  `json:"expiryTime"`
	Flow       string `json:"flow,omitempty"`
	Group      string `json:"group,omitempty"`
	LimitIP    int    `json:"limitIp"`
	Reset      int    `json:"reset"`
	Security   string `json:"security,omitempty"`
	SubID      string `json:"subId"`
	TgID       int64  `json:"tgId"`
	TotalGB    int64  `json:"totalGB"`
	Comment    string `json:"comment,omitempty"`
	Password   string `json:"password,omitempty"`
	Auth       string `json:"auth,omitempty"`
}

type AddClientRequest struct {
	Client     ClientConfig `json:"client"`
	InboundIDs []int        `json:"inboundIds"`
}

type UpdateClientRequest struct {
	Client ClientConfig `json:"client"`
}

type attachRequest struct {
	InboundIDs []int `json:"inboundIds"`
}

type ClientTraffic struct {
	ID         int    `json:"id"`
	InboundID  int    `json:"inboundId"`
	Email      string `json:"email"`
	Enable     bool   `json:"enable"`
	Up         int64  `json:"up"`
	Down       int64  `json:"down"`
	Total      int64  `json:"total"`
	ExpiryTime int64  `json:"expiryTime"`
	Reset      int    `json:"reset"`
	SubID      string `json:"subId"`
	UUID       string `json:"uuid"`
	LastOnline int64  `json:"lastOnline"`
}

type XUIClientInfo struct {
	ID         int    `json:"id"`
	Email      string `json:"email"`
	SubID      string `json:"subId"`
	UUID       string `json:"uuid"`
	Password   string `json:"password"`
	TotalGB    int64  `json:"totalGB"`
	ExpiryTime int64  `json:"expiryTime"`
	Enable     bool   `json:"enable"`
	InboundIDs []int  `json:"inboundIds"`
	LimitIP    int    `json:"limitIp"`
}

