package providers

import (
	"encoding/base64"
	"net/http"
)

// Auth is the non-secret connection description plus the secret read from the credential store.
type Auth struct {
	Provider, Deployment, Method string
	Username                     string
	Secret                       string
}

// Apply sets the provider's authentication header. The secret never leaves this function except in the header.
func (a Auth) Apply(req *http.Request) error {
	basic := func() error {
		if a.Username == "" {
			return newError(KindConfig, "username_required", "this authentication method needs the account username or email")
		}
		req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(a.Username+":"+a.Secret)))
		return nil
	}
	unsupported := newError(KindConfig, "auth_method_unsupported", "authentication method "+a.Method+" is not supported for this provider")
	switch a.Provider {
	case "gitlab":
		switch a.Method {
		case "personal_access_token", "api_token":
			req.Header.Set("PRIVATE-TOKEN", a.Secret)
		case "oauth":
			req.Header.Set("Authorization", "Bearer "+a.Secret)
		default:
			return unsupported
		}
	case "jira":
		if a.Deployment == "cloud" {
			switch a.Method {
			case "api_token", "basic":
				return basic()
			case "oauth":
				req.Header.Set("Authorization", "Bearer "+a.Secret)
			default:
				return unsupported
			}
			return nil
		}
		switch a.Method {
		case "personal_access_token", "api_token", "oauth":
			req.Header.Set("Authorization", "Bearer "+a.Secret)
		case "basic":
			return basic()
		default:
			return unsupported
		}
	case "confluence":
		if a.Deployment == "cloud" {
			switch a.Method {
			case "api_token", "basic":
				return basic()
			case "oauth":
				req.Header.Set("Authorization", "Bearer "+a.Secret)
			default:
				return unsupported
			}
			return nil
		}
		switch a.Method {
		case "personal_access_token", "api_token", "oauth":
			req.Header.Set("Authorization", "Bearer "+a.Secret)
		case "basic":
			return basic()
		default:
			return unsupported
		}
	default:
		return unsupported
	}
	return nil
}
