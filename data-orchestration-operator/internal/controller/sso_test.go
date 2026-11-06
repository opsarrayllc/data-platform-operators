package controller

import (
	"strings"
	"testing"
)

func TestControllerConfigKeepsIssuerAndGroupsClaim(t *testing.T) {
	cfg := controllerConfig(oidcConfig{
		enabled:      true,
		publicIssuer: "https://oauth.id.jumpcloud.com/",
		groupsClaim:  "memberOf",
		publicURL:    "https://argo.example.com",
	})
	for _, want := range []string{
		"issuer: https://oauth.id.jumpcloud.com/\n",
		"customGroupClaimName: memberOf\n",
		"redirectUrl: https://argo.example.com/oauth2/callback\n",
	} {
		if !strings.Contains(cfg, want) {
			t.Errorf("controller config missing %q:\n%s", want, cfg)
		}
	}
}
