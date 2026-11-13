package httpserver

import (
	"encoding/json"
	"errors"
	"net/netip"

	"github.com/gin-gonic/gin"
)

func configureProxies(router *gin.Engine, raw string) error {
	if raw == "" {
		return router.SetTrustedProxies(nil)
	}
	var addresses []string
	if len(raw) > 1024 || json.Unmarshal([]byte(raw), &addresses) != nil || len(addresses) == 0 || len(addresses) > 8 {
		return errors.New("invalid trusted proxy addresses")
	}
	for _, value := range addresses {
		address, err := netip.ParseAddr(value)
		if err != nil || address.Zone() != "" || !address.IsGlobalUnicast() && !address.IsLoopback() {
			return errors.New("trusted proxy must be an exact unicast IP")
		}
	}
	return router.SetTrustedProxies(addresses)
}
