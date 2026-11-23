package http

import (
	"encoding/base64"
	"encoding/json"
	"strconv"
	"time"

	"github.com/WeijianHuang/porch-os/internal/adapter/postgres"
	apierrors "github.com/WeijianHuang/porch-os/internal/platform/errors"
	"github.com/gin-gonic/gin"
)

type cursor struct {
	T time.Time `json:"t"`
	I int64     `json:"i"`
}

func parseList(c *gin.Context) (int, *time.Time, int64, error) {
	limit := 20
	if raw := c.Query("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 100 {
			return 0, nil, 0, apierrors.InvalidArgument
		}
		limit = n
	}
	sortBy := c.Query("sort")
	if sortBy != "" && sortBy != "created_at_desc" {
		return 0, nil, 0, apierrors.InvalidArgument // whitelist; never concatenated into SQL
	}
	if raw := c.Query("cursor"); raw != "" {
		b, err := base64.RawURLEncoding.DecodeString(raw)
		if err != nil {
			return 0, nil, 0, apierrors.InvalidArgument
		}
		var cur cursor
		if err := json.Unmarshal(b, &cur); err != nil || cur.I <= 0 {
			return 0, nil, 0, apierrors.InvalidArgument
		}
		return limit, &cur.T, cur.I, nil
	}
	return limit, nil, 0, nil
}

func pageResponse(last any, page postgres.Page) gin.H {
	out := gin.H{"has_more": page.HasMore}
	if page.HasMore && last != nil {
		if item, ok := last.(interface{ GetCursor() (time.Time, int64) }); ok {
			t, id := item.GetCursor()
			b, _ := json.Marshal(cursor{T: t, I: id})
			out["next_cursor"] = base64.RawURLEncoding.EncodeToString(b)
		}
	}
	return out
}
