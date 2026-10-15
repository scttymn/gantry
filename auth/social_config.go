package auth

import (
	"errors"
	"os"
	"strconv"
)

// SocialFromEnv loads optional Google/Apple credentials at app startup. No
// credentials means nil (existing auth is unchanged). Partial configuration is
// an error. Never print the returned configuration or the private key.
func SocialFromEnv(getenv func(string) string) (*Social, error) {
	social := &Social{}
	if raw := getenv("AUTH_SOCIAL_SIGNUP"); raw != "" {
		signup, err := strconv.ParseBool(raw)
		if err != nil {
			return nil, errors.New("auth: AUTH_SOCIAL_SIGNUP must be true or false")
		}
		social.Signup = signup
	}
	google := GoogleConfig{ClientID: getenv("GOOGLE_CLIENT_ID"), ClientSecret: getenv("GOOGLE_CLIENT_SECRET"), RedirectURL: getenv("GOOGLE_REDIRECT_URL")}
	if google.ClientID != "" || google.ClientSecret != "" || google.RedirectURL != "" {
		p, err := NewGoogle(google)
		if err != nil {
			return nil, err
		}
		social.Providers = append(social.Providers, p)
	}
	apple := AppleConfig{ClientID: getenv("APPLE_CLIENT_ID"), TeamID: getenv("APPLE_TEAM_ID"), KeyID: getenv("APPLE_KEY_ID"), RedirectURL: getenv("APPLE_REDIRECT_URL"), PrivateKey: []byte(getenv("APPLE_PRIVATE_KEY"))}
	if file := getenv("APPLE_PRIVATE_KEY_FILE"); file != "" {
		if len(apple.PrivateKey) > 0 {
			return nil, errors.New("auth: configure APPLE_PRIVATE_KEY or APPLE_PRIVATE_KEY_FILE, not both")
		}
		key, err := os.ReadFile(file)
		if err != nil {
			return nil, errors.New("auth: could not read APPLE_PRIVATE_KEY_FILE")
		}
		apple.PrivateKey = key
	}
	if apple.ClientID != "" || apple.TeamID != "" || apple.KeyID != "" || apple.RedirectURL != "" || len(apple.PrivateKey) > 0 {
		p, err := NewApple(apple)
		if err != nil {
			return nil, err
		}
		social.Providers = append(social.Providers, p)
	}
	if len(social.Providers) == 0 {
		return nil, nil
	}
	return social, nil
}
