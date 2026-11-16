// Package http adapts HTTP requests to use cases. Handlers only parse input,
// call a repository/use case, and format the response (design/04 §2).
package http

import (
	"crypto/rand"
	"encoding/hex"

	"github.com/WeijianHuang/porch-os/internal/domain/shop"
	apierrors "github.com/WeijianHuang/porch-os/internal/platform/errors"
	"github.com/gin-gonic/gin"
)

type Principal struct {
	UserID    int64
	Login     string
	Roles     []string
	Perms     []string
	Visible   []shop.Visible
	ShopID    int64
	ShopPID   string
	RequestID string
	IdemKey   string
	Body      []byte
}

const principalKey = "principal"

func Current(c *gin.Context) *Principal {
	v, ok := c.Get(principalKey)
	if !ok {
		p := &Principal{}
		c.Set(principalKey, p)
		return p
	}
	return v.(*Principal)
}

func setPrincipal(c *gin.Context, p *Principal) { c.Set(principalKey, p) }

func NewRequestID(fromHeader string) string {
	if fromHeader != "" && len(fromHeader) <= 128 {
		return fromHeader
	}
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return "req-fallback"
	}
	return "req_" + hex.EncodeToString(b)
}

func ok(c *gin.Context, status int, data any, page any) {
	body := gin.H{"data": data, "meta": gin.H{"request_id": Current(c).RequestID}}
	if page != nil {
		body["page"] = page
	}
	c.JSON(status, body)
}

func fail(c *gin.Context, err error) {
	e := apierrors.As(err)
	if e.RetryAfter > 0 {
		c.Header("Retry-After", string(rune('0'+e.RetryAfter)))
	}
	c.JSON(e.Status, gin.H{"error": e, "meta": gin.H{"request_id": Current(c).RequestID}})
}

func hasPerm(p *Principal, code string) bool {
	for _, v := range p.Perms {
		if v == code {
			return true
		}
	}
	return false
}

func badRequest(c *gin.Context, err error) {
	fail(c, apierrors.WithDetails(apierrors.InvalidArgument, apierrors.Detail{Field: "body", Reason: err.Error()}))
}
