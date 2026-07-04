package middleware

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/common/limiter"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/setting"

	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
)

const (
	ModelRequestRateLimitCountMark        = "MRRL"
	ModelRequestRateLimitSuccessCountMark = "MRRLS"
)

// 检查Redis中的请求限制
// retryAfter is the seconds until the oldest recorded request slides out of the
// window (the real wait), 0 when allowed or unknown (caller falls back to the
// full window).
func checkRedisRateLimit(ctx context.Context, rdb *redis.Client, key string, maxCount int, duration int64) (bool, int64, error) {
	// 如果maxCount为0，表示不限制
	if maxCount == 0 {
		return true, 0, nil
	}

	// 获取当前计数
	length, err := rdb.LLen(ctx, key).Result()
	if err != nil {
		return false, 0, err
	}

	// 如果未达到限制，允许请求
	if length < int64(maxCount) {
		return true, 0, nil
	}

	// 检查时间窗口
	oldTimeStr, _ := rdb.LIndex(ctx, key, -1).Result()
	oldTime, err := time.Parse(timeFormat, oldTimeStr)
	if err != nil {
		return false, 0, err
	}

	nowTimeStr := time.Now().Format(timeFormat)
	nowTime, err := time.Parse(timeFormat, nowTimeStr)
	if err != nil {
		return false, 0, err
	}
	// 如果在时间窗口内已达到限制，拒绝请求
	subTime := nowTime.Sub(oldTime).Seconds()
	if int64(subTime) < duration {
		rdb.Expire(ctx, key, time.Duration(setting.ModelRequestRateLimitDurationMinutes)*time.Minute)
		remaining := duration - int64(subTime)
		if remaining < 1 {
			remaining = 1
		}
		return false, remaining, nil
	}

	return true, 0, nil
}

// 记录Redis请求
func recordRedisRequest(ctx context.Context, rdb *redis.Client, key string, maxCount int) {
	// 如果maxCount为0，不记录请求
	if maxCount == 0 {
		return
	}

	now := time.Now().Format(timeFormat)
	rdb.LPush(ctx, key, now)
	rdb.LTrim(ctx, key, 0, int64(maxCount-1))
	rdb.Expire(ctx, key, time.Duration(setting.ModelRequestRateLimitDurationMinutes)*time.Minute)
}

// Redis限流处理器
func redisRateLimitHandler(duration int64, totalMaxCount, successMaxCount int) gin.HandlerFunc {
	return func(c *gin.Context) {
		userId := strconv.Itoa(c.GetInt("id"))
		ctx := context.Background()
		rdb := common.RDB

		// 1. 检查成功请求数限制
		successKey := fmt.Sprintf("rateLimit:%s:%s", ModelRequestRateLimitSuccessCountMark, userId)
		allowed, retryAfter, err := checkRedisRateLimit(ctx, rdb, successKey, successMaxCount, duration)
		if err != nil {
			fmt.Println("failed to check success request rate limit:", err.Error())
			abortWithOpenAiMessage(c, http.StatusInternalServerError, "rate_limit_check_failed")
			return
		}
		if !allowed {
			if retryAfter <= 0 {
				retryAfter = duration
			}
			c.Header("Retry-After", strconv.FormatInt(retryAfter, 10))
			abortWithOpenAiMessage(c, http.StatusTooManyRequests, i18n.T(c, "rate_limit.reached", map[string]any{"Minutes": setting.ModelRequestRateLimitDurationMinutes, "Count": successMaxCount}))
			return
		}

		//2.检查总请求数限制并记录总请求（当totalMaxCount为0时会自动跳过，使用令牌桶限流器
		if totalMaxCount > 0 {
			totalKey := fmt.Sprintf("rateLimit:%s", userId)
			// 初始化
			tb := limiter.New(ctx, rdb)
			allowed, err = tb.Allow(
				ctx,
				totalKey,
				limiter.WithCapacity(int64(totalMaxCount)*duration),
				limiter.WithRate(int64(totalMaxCount)),
				limiter.WithRequested(duration),
			)

			if err != nil {
				fmt.Println("failed to check total request rate limit:", err.Error())
				abortWithOpenAiMessage(c, http.StatusInternalServerError, "rate_limit_check_failed")
				return
			}

			if !allowed {
				abortWithOpenAiMessage(c, http.StatusTooManyRequests, i18n.T(c, "rate_limit.total_reached", map[string]any{"Minutes": setting.ModelRequestRateLimitDurationMinutes, "Count": totalMaxCount}))
			}
		}

		// 4. 处理请求
		c.Next()

		// 5. 如果请求成功，记录成功请求
		if c.Writer.Status() < 400 {
			recordRedisRequest(ctx, rdb, successKey, successMaxCount)
		}
	}
}

// 内存限流处理器
func memoryRateLimitHandler(duration int64, totalMaxCount, successMaxCount int) gin.HandlerFunc {
	inMemoryRateLimiter.Init(time.Duration(setting.ModelRequestRateLimitDurationMinutes) * time.Minute)

	return func(c *gin.Context) {
		userId := strconv.Itoa(c.GetInt("id"))
		totalKey := ModelRequestRateLimitCountMark + userId
		successKey := ModelRequestRateLimitSuccessCountMark + userId

		// 1. 检查总请求数限制（当totalMaxCount为0时跳过）
		if totalMaxCount > 0 && !inMemoryRateLimiter.Request(totalKey, totalMaxCount, duration) {
			c.Status(http.StatusTooManyRequests)
			c.Abort()
			return
		}

		// 2. 检查成功请求数限制
		// 使用一个临时key来检查限制，这样可以避免实际记录
		checkKey := successKey + "_check"
		if !inMemoryRateLimiter.Request(checkKey, successMaxCount, duration) {
			c.Status(http.StatusTooManyRequests)
			c.Abort()
			return
		}

		// 3. 处理请求
		c.Next()

		// 4. 如果请求成功，记录到实际的成功请求计数中
		if c.Writer.Status() < 400 {
			inMemoryRateLimiter.Request(successKey, successMaxCount, duration)
		}
	}
}

const (
	perModelRateLimitKeyMark = "perModelRateLimitKey"
	perModelRateLimitMaxMark = "perModelRateLimitMax"
)

// isNewUser reports whether the account should be throttled harder: too young OR
// too little spent (either configured threshold matches). Unset thresholds = off.
func isNewUser(c *gin.Context) bool {
	maxAgeDays := setting.ModelRequestRateLimitNewUserMaxAgeDays
	maxUsedQuota := setting.ModelRequestRateLimitNewUserMaxUsedQuota
	if maxAgeDays <= 0 && maxUsedQuota <= 0 {
		return false
	}
	if maxAgeDays > 0 {
		createdAt := c.GetInt64(string(constant.ContextKeyUserCreatedAt))
		if createdAt > 0 && (time.Now().Unix()-createdAt)/86400 < int64(maxAgeDays) {
			return true
		}
	}
	if maxUsedQuota > 0 {
		if common.GetContextKeyInt(c, constant.ContextKeyUserUsedQuota) < maxUsedQuota {
			return true
		}
	}
	return false
}

// scaleForNewUser multiplies a count by the new-user factor (floored at 1).
func scaleForNewUser(count int) int {
	factor := setting.ModelRequestRateLimitNewUserFactor
	if factor >= 1 || count <= 0 {
		return count
	}
	scaled := int(float64(count) * factor)
	if scaled < 1 {
		scaled = 1
	}
	return scaled
}

// perModelRateLimit enforces a per-user, per-model success-count cap for the
// configured `:free` models. Returns false when the request was blocked (already
// aborted). Paid/small models (not in the map) return true unchanged. On allow it
// stashes the key/max so the post-handler records the request only on success.
func perModelRateLimit(c *gin.Context) bool {
	if !setting.HasModelRateLimits() {
		return true
	}
	// Exempt admin/root (autotest probes, sync, dashboards: limiting them would
	// falsely 429 channel tests and ban healthy channels) and users an admin
	// explicitly granted unlimited free models (per-user setting; balance alone
	// no longer bypasses - the signup grant made every fresh account exempt).
	if common.GetContextKeyInt(c, constant.ContextKeyUserRole) >= common.RoleAdminUser {
		return true
	}
	if s, ok := common.GetContextKeyType[dto.UserSetting](c, constant.ContextKeyUserSetting); ok && s.UnlimitedFreeModels {
		return true
	}
	var mr ModelRequest
	if err := common.UnmarshalBodyReusable(c, &mr); err != nil || mr.Model == "" {
		return true
	}
	_, successMaxCount, found := setting.GetModelRateLimit(mr.Model)
	if !found || successMaxCount <= 0 {
		return true
	}

	// The new-user 429 message claims a REDUCED limit, so only use it when
	// scaling actually lowered the cap (the factor floors at 1, so on 1/min
	// models an old low-spend account would otherwise see a false claim).
	newUser := false
	if isNewUser(c) {
		scaled := scaleForNewUser(successMaxCount)
		newUser = scaled < successMaxCount
		successMaxCount = scaled
	}

	duration := int64(setting.ModelRequestRateLimitDurationMinutes * 60)
	userId := strconv.Itoa(c.GetInt("id"))
	key := fmt.Sprintf("rateLimit:MODEL:%s:%s", userId, mr.Model)

	allowed := true
	// Real remaining wait from the limiter (0 = unknown -> full window fallback).
	var retryAfter int64
	if common.RedisEnabled {
		ok, remaining, err := checkRedisRateLimit(context.Background(), common.RDB, key, successMaxCount, duration)
		if err == nil {
			allowed = ok
			retryAfter = remaining
		}
	} else {
		inMemoryRateLimiter.Init(time.Duration(setting.ModelRequestRateLimitDurationMinutes) * time.Minute)
		allowed = inMemoryRateLimiter.Request(key+"_check", successMaxCount, duration)
	}

	if !allowed {
		paidName := strings.TrimSuffix(mr.Model, ":free")
		if retryAfter <= 0 {
			retryAfter = duration
		}
		c.Header("Retry-After", strconv.FormatInt(retryAfter, 10))
		c.Header("X-RateLimit-Limit", strconv.Itoa(successMaxCount))
		c.Header("X-RateLimit-Remaining", "0")
		c.Header("X-RateLimit-Reset", strconv.FormatInt(time.Now().Unix()+retryAfter, 10))
		var msg string
		if newUser {
			msg = i18n.T(c, "rate_limit.new_user_reached", map[string]any{
				"Seconds": retryAfter, "Paid": paidName,
			})
		} else {
			msg = i18n.T(c, "rate_limit.free_model_reached", map[string]any{
				"Model": mr.Model, "Count": successMaxCount,
				"Minutes": setting.ModelRequestRateLimitDurationMinutes,
				"Seconds": retryAfter, "Paid": paidName,
			})
		}
		abortWithOpenAiMessage(c, http.StatusTooManyRequests, msg)
		return false
	}

	c.Set(perModelRateLimitKeyMark, key)
	c.Set(perModelRateLimitMaxMark, successMaxCount)
	return true
}

// recordPerModelSuccess records a successful per-model request after the handler
// runs, so failed upstream calls never burn the user's budget.
func recordPerModelSuccess(c *gin.Context) {
	key := c.GetString(perModelRateLimitKeyMark)
	maxCount := c.GetInt(perModelRateLimitMaxMark)
	if key == "" || maxCount <= 0 || c.Writer.Status() >= 400 {
		return
	}
	if common.RedisEnabled {
		recordRedisRequest(context.Background(), common.RDB, key, maxCount)
	} else {
		inMemoryRateLimiter.Request(key, maxCount, int64(setting.ModelRequestRateLimitDurationMinutes*60))
	}
}

// ModelRequestRateLimit 模型请求限流中间件
func ModelRequestRateLimit() func(c *gin.Context) {
	return func(c *gin.Context) {
		// 在每个请求时检查是否启用限流
		if !setting.ModelRequestRateLimitEnabled {
			c.Next()
			return
		}

		if !perModelRateLimit(c) {
			return
		}
		defer recordPerModelSuccess(c)

		// 计算限流参数
		duration := int64(setting.ModelRequestRateLimitDurationMinutes * 60)
		totalMaxCount := setting.ModelRequestRateLimitCount
		successMaxCount := setting.ModelRequestRateLimitSuccessCount

		// 获取分组
		group := common.GetContextKeyString(c, constant.ContextKeyTokenGroup)
		if group == "" {
			group = common.GetContextKeyString(c, constant.ContextKeyUserGroup)
		}

		//获取分组的限流配置
		groupTotalCount, groupSuccessCount, found := setting.GetGroupRateLimit(group)
		if found {
			totalMaxCount = groupTotalCount
			successMaxCount = groupSuccessCount
		}

		// 根据存储类型选择并执行限流处理器
		if common.RedisEnabled {
			redisRateLimitHandler(duration, totalMaxCount, successMaxCount)(c)
		} else {
			memoryRateLimitHandler(duration, totalMaxCount, successMaxCount)(c)
		}
	}
}
