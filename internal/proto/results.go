package proto

// RegisterRequest is POSTed to /v1/register (TLS without client certificate).
type RegisterRequest struct {
	Token    string `json:"token"`
	Name     string `json:"name"`
	Hostname string `json:"hostname"`
	CSR      string `json:"csr"`
}

// RegisterResponse carries the issued certificate.
type RegisterResponse struct {
	AgentID string `json:"agent_id"`
	Cert    string `json:"cert"`
	CA      string `json:"ca"`
}

// RenewRequest is POSTed to /v1/renew over mTLS.
type RenewRequest struct {
	CSR string `json:"csr"`
}
