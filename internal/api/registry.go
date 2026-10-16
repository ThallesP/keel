package api

type Registry struct {
	Host     string `json:"host" doc:"Registry address, the prefix of every image in it" example:"100.64.0.7:5000"`
	Username string `json:"username" example:"keel"`
	Password string `json:"password"`
	CA       string `json:"ca" doc:"PEM certificate of the CA that signed the registry's certificate"`
}
