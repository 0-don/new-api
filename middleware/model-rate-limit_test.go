package middleware

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/setting"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetModelRateLimit(t *testing.T) {
	require.NoError(t, setting.UpdateModelRequestRateLimitModelsByJSONString(`{"kimi-k2.6:free":[0,20],"glm-4.5-flash:free":[5,15]}`))

	total, success, found := setting.GetModelRateLimit("kimi-k2.6:free")
	assert.True(t, found)
	assert.Equal(t, 0, total)
	assert.Equal(t, 20, success)

	_, _, found = setting.GetModelRateLimit("kimi-k2.6") // paid twin not in map
	assert.False(t, found)

	_, _, found = setting.GetModelRateLimit("gpt-5.5")
	assert.False(t, found)
}

// newPerModelCtx builds a POST ctx with the model body + user identity the
// per-model limiter reads. recorder returned for status/header assertions.
func newPerModelCtx(userId, role, quota int, userSetting dto.UserSetting, model string) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	body := []byte(`{"model":"` + model + `"}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("id", userId)
	c.Set(string(constant.ContextKeyUserRole), role)
	c.Set(string(constant.ContextKeyUserQuota), quota)
	c.Set(string(constant.ContextKeyUserSetting), userSetting)
	c.Set(string(constant.ContextKeyUserCreatedAt), time.Now().Unix()-30*86400)
	c.Set(string(constant.ContextKeyUserUsedQuota), 10_000_000)
	return c, w
}

// Contract: nobody bypasses the per-model free limits by balance anymore; only
// admins and users with the per-user UnlimitedFreeModels grant are exempt.
func TestPerModelRateLimitBypassSemantics(t *testing.T) {
	require.NoError(t, setting.UpdateModelRequestRateLimitModelsByJSONString(`{"limited-model:free":[0,1]}`))
	setting.ModelRequestRateLimitDurationMinutes = 1
	// RedisEnabled defaults true; no Redis in tests -> force the in-memory limiter.
	prevRedis := common.RedisEnabled
	common.RedisEnabled = false
	t.Cleanup(func() { common.RedisEnabled = prevRedis })
	// The blocked path renders an i18n 429 message.
	require.NoError(t, i18n.Init())

	run := func(userId, role, quota int, s dto.UserSetting) (first, second bool, w2 *httptest.ResponseRecorder) {
		c1, _ := newPerModelCtx(userId, role, quota, s, "limited-model:free")
		first = perModelRateLimit(c1)
		c2, w := newPerModelCtx(userId, role, quota, s, "limited-model:free")
		second = perModelRateLimit(c2)
		return first, second, w
	}

	t.Run("zero-balance user hits the limit", func(t *testing.T) {
		first, second, w2 := run(910001, common.RoleCommonUser, 0, dto.UserSetting{})
		assert.True(t, first)
		assert.False(t, second)
		assert.Equal(t, http.StatusTooManyRequests, w2.Code)
		assert.NotEmpty(t, w2.Header().Get("Retry-After"))
	})

	t.Run("funded user no longer bypasses", func(t *testing.T) {
		first, second, w2 := run(910002, common.RoleCommonUser, 5_000_000, dto.UserSetting{})
		assert.True(t, first)
		assert.False(t, second)
		assert.Equal(t, http.StatusTooManyRequests, w2.Code)
	})

	t.Run("unlimited-free grant bypasses", func(t *testing.T) {
		first, second, _ := run(910003, common.RoleCommonUser, 0, dto.UserSetting{UnlimitedFreeModels: true})
		assert.True(t, first)
		assert.True(t, second)
	})

	t.Run("admin no longer bypasses (guest token abuse)", func(t *testing.T) {
		first, second, w2 := run(910004, common.RoleAdminUser, 0, dto.UserSetting{})
		assert.True(t, first)
		assert.False(t, second)
		assert.Equal(t, http.StatusTooManyRequests, w2.Code)
	})
}

