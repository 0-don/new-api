package controller

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/authz"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"

	"github.com/QuantumNous/new-api/constant"

	"github.com/gin-contrib/sessions"
	"github.com/gin-gonic/gin"
	"github.com/go-fuego/fuego"
	"gorm.io/gorm"
)

// Login uses *gin.Context because setupLogin writes session + JSON directly
func Login(c *gin.Context) {
	if !common.PasswordLoginEnabled {
		common.ApiErrorI18n(c, "user.password_login_disabled")
		return
	}
	var loginRequest dto.LoginRequest
	err := json.NewDecoder(c.Request.Body).Decode(&loginRequest)
	if err != nil {
		common.ApiErrorI18n(c, "common.invalid_params")
		return
	}
	username := loginRequest.Username
	password := loginRequest.Password
	if username == "" || password == "" {
		common.ApiErrorI18n(c, "common.invalid_params")
		return
	}
	user := model.User{
		Username: username,
		Password: password,
	}
	err = user.ValidateAndFill()
	if err != nil {
		switch {
		case errors.Is(err, model.ErrDatabase):
			common.SysLog(fmt.Sprintf("Login database error for user %s: %v", username, err))
			common.ApiErrorI18n(c, i18n.MsgDatabaseError)
		case errors.Is(err, model.ErrUserEmptyCredentials):
			common.ApiErrorI18n(c, i18n.MsgInvalidParams)
		default:
			common.ApiErrorI18n(c, i18n.MsgUserUsernameOrPasswordError)
		}
		return
	}

	if model.IsTwoFAEnabled(user.Id) {
		session := sessions.Default(c)
		session.Set("pending_username", user.Username)
		session.Set("pending_user_id", user.Id)
		err := session.Save()
		if err != nil {
			common.ApiErrorI18n(c, "user.session_save_failed")
			return
		}

		c.JSON(http.StatusOK, dto.ApiResponse{
			Success: true,
			Message: i18n.T(c, "user.require_2fa"),
			Data:    dto.Login2FAData{Require2FA: true},
		})
		return
	}

	setupLogin(&user, c)
}

// loginMethodFromContext 根据请求路径推导登录方式，用于登录审计日志。
func loginMethodFromContext(c *gin.Context) string {
	switch c.FullPath() {
	case "/api/user/login":
		return "password"
	case "/api/user/login/2fa":
		return "2fa"
	case "/api/user/passkey/login/finish":
		return "passkey"
	case "/api/oauth/wechat":
		return "wechat"
	case "/api/oauth/telegram/login":
		return "telegram"
	case "/api/oauth/:provider":
		if provider := c.Param("provider"); provider != "" {
			return "oauth:" + provider
		}
		return "oauth"
	default:
		return "unknown"
	}
}

// recordLoginAudit 记录登录成功审计日志（对所有用户启用，仅记录成功，不记录失败）。
func recordLoginAudit(user *model.User, c *gin.Context) {
	method := loginMethodFromContext(c)
	ip := c.ClientIP()
	extra := map[string]interface{}{
		"login_method": method,
		"user_agent":   c.Request.UserAgent(),
	}
	content := fmt.Sprintf("Logged in successfully via %s", method)
	model.RecordLoginLog(user.Id, user.Username, content, ip, "login", map[string]interface{}{
		"method": method,
	}, extra)
}

// setupLogin sets session & cookies and returns user info
func setupLogin(user *model.User, c *gin.Context) {
	model.UpdateUserLastLoginAt(user.Id)
	session := sessions.Default(c)
	session.Set("id", user.Id)
	session.Set("username", user.Username)
	session.Set("role", user.Role)
	session.Set("status", user.Status)
	session.Set("group", user.Group)
	err := session.Save()
	if err != nil {
		common.ApiErrorI18n(c, "user.session_save_failed")
		return
	}
	recordLoginAudit(user, c)
	c.JSON(http.StatusOK, dto.ApiResponse{
		Success: true,
		Message: "",
		Data: dto.LoginData{
			ID:          user.Id,
			Username:    user.Username,
			DisplayName: user.DisplayName,
			Role:        user.Role,
			Status:      user.Status,
			Group:       user.Group,
		},
	})
}

// setupLoginAndRedirect generates a one-time exchange code and returns a redirect URL.
// The external frontend exchanges the code for user data via POST /api/oauth/exchange.
func setupLoginAndRedirect(user *model.User, c *gin.Context, redirectURI string) {
	accessToken := user.GetAccessToken()
	if accessToken == "" {
		key, err := common.GenerateRandomKey(32)
		if err != nil {
			common.ApiError(c, err)
			return
		}
		user.SetAccessToken(key)
		if err := user.Update(false); err != nil {
			common.ApiError(c, err)
			return
		}
		accessToken = key
	}

	code, err := common.StoreOAuthExchangeCode(&common.OAuthExchangeData{
		AccessToken: accessToken,
		UserID:      user.Id,
		Username:    user.Username,
		DisplayName: user.DisplayName,
		Role:        user.Role,
	})
	if err != nil {
		common.ApiError(c, err)
		return
	}

	parsed, _ := url.Parse(redirectURI)
	q := parsed.Query()
	q.Set("code", code)
	parsed.RawQuery = q.Encode()

	// Provider callbacks are top-level browser navigations, so 302 the browser to
	// the frontend callback instead of returning JSON it would just render.
	c.Redirect(http.StatusFound, parsed.String())
}

// setupBindAndRedirect generates a one-time exchange code with action=bind and returns a redirect URL.
func setupBindAndRedirect(user *model.User, c *gin.Context, redirectURI string) {
	code, err := common.StoreOAuthExchangeCode(&common.OAuthExchangeData{
		UserID:      user.Id,
		Username:    user.Username,
		DisplayName: user.DisplayName,
		Role:        user.Role,
		Action:      "bind",
	})
	if err != nil {
		common.ApiError(c, err)
		return
	}

	parsed, _ := url.Parse(redirectURI)
	q := parsed.Query()
	q.Set("code", code)
	parsed.RawQuery = q.Encode()

	c.Redirect(http.StatusFound, parsed.String())
}

// setupOAuthErrorRedirect sends the external frontend a redirect URL carrying
// the error, so a failed bind/login (e.g. already_bound) bounces back to the
// originating frontend instead of stranding the user on the API host. Returns
// true when the external-redirect flow was handled.
func setupOAuthErrorRedirect(c *gin.Context, redirectURI, errMsg string) bool {
	if redirectURI == "" {
		return false
	}
	parsed, err := url.Parse(redirectURI)
	if err != nil {
		return false
	}
	q := parsed.Query()
	q.Set("error", errMsg)
	parsed.RawQuery = q.Encode()

	c.Redirect(http.StatusFound, parsed.String())
	return true
}

// ExchangeOAuthCode exchanges a one-time OAuth code for user data and access token.
func ExchangeOAuthCode(c fuego.ContextWithBody[dto.OAuthExchangeRequest]) (*dto.Response[dto.OAuthExchangeData], error) {
	ginCtx := dto.GinCtx(c)
	body, err := c.Body()
	if err != nil || body.Code == "" {
		return dto.Fail[dto.OAuthExchangeData](common.TranslateMessage(ginCtx, "oauth.invalid_code"))
	}

	data := common.RedeemOAuthExchangeCode(body.Code)
	if data == nil {
		return dto.Fail[dto.OAuthExchangeData](common.TranslateMessage(ginCtx, "oauth.invalid_or_expired_code"))
	}

	return dto.Ok(dto.OAuthExchangeData{
		AccessToken: data.AccessToken,
		UserID:      data.UserID,
		Username:    data.Username,
		DisplayName: data.DisplayName,
		Role:        data.Role,
		Action:      data.Action,
	})
}

func Logout(c fuego.ContextNoBody) (dto.MessageResponse, error) {
	session := sessions.Default(dto.GinCtx(c))
	session.Clear()
	if err := session.Save(); err != nil {
		return dto.FailMsg(err.Error())
	}
	return dto.Msg("")
}

func Register(c fuego.ContextWithBody[dto.RegisterRequest]) (dto.MessageResponse, error) {
	ginCtx := dto.GinCtx(c)
	if !common.RegisterEnabled {
		return dto.FailMsg(common.TranslateMessage(ginCtx, "user.register_disabled"))
	}
	if !common.PasswordRegisterEnabled {
		return dto.FailMsg(common.TranslateMessage(ginCtx, "user.password_register_disabled"))
	}
	req, err := c.Body()
	if err != nil {
		return dto.FailMsg(common.TranslateMessage(ginCtx, "common.invalid_params"))
	}
	if err := common.Validate.Struct(&req); err != nil {
		return dto.FailMsg(common.TranslateMessage(ginCtx, "user.input_invalid", map[string]any{"Error": err.Error()}))
	}
	if common.EmailVerificationEnabled {
		if req.Email == "" || req.VerificationCode == "" {
			return dto.FailMsg(common.TranslateMessage(ginCtx, "user.email_verification_required"))
		}
		if !common.VerifyCodeWithKey(req.Email, req.VerificationCode, common.EmailVerificationPurpose) {
			return dto.FailMsg(common.TranslateMessage(ginCtx, "user.verification_code_error"))
		}
	}
	exist, err := model.CheckUserExistOrDeleted(req.Username, req.Email)
	if err != nil {
		common.SysLog(fmt.Sprintf(i18n.Translate("ctrl.checkuserexistordeleted_error"), err))
		return dto.FailMsg(common.TranslateMessage(ginCtx, "common.database_error"))
	}
	if exist {
		return dto.FailMsg(common.TranslateMessage(ginCtx, "user.exists"))
	}
	affCode := req.AffCode
	inviterId, _ := model.GetUserIdByAffCode(affCode)
	cleanUser := model.User{
		Username:    req.Username,
		Password:    req.Password,
		DisplayName: req.Username,
		InviterId:   inviterId,
		Role:        common.RoleCommonUser,
	}
	if common.EmailVerificationEnabled {
		cleanUser.Email = req.Email
	}
	if err := cleanUser.Insert(inviterId); err != nil {
		return dto.FailMsg(err.Error())
	}

	var insertedUser model.User
	if err := model.DB.Where("username = ?", cleanUser.Username).First(&insertedUser).Error; err != nil {
		return dto.FailMsg(common.TranslateMessage(ginCtx, "user.register_failed"))
	}
	if constant.GenerateDefaultToken {
		key, err := common.GenerateKey()
		if err != nil {
			common.SysLog(i18n.Translate("ctrl.failed_to_generate_token_key") + err.Error())
			return dto.FailMsg(common.TranslateMessage(ginCtx, "user.default_token_failed"))
		}
		token := model.Token{
			UserId:             insertedUser.Id,
			Name:               i18n.Translate("user.initial_token_name", map[string]any{"Username": cleanUser.Username}),
			Key:                key,
			CreatedTime:        common.GetTimestamp(),
			AccessedTime:       common.GetTimestamp(),
			ExpiredTime:        -1,
			RemainQuota:        500000,
			UnlimitedQuota:     true,
			ModelLimitsEnabled: false,
		}
		if setting.DefaultUseAutoGroup {
			token.Group = "auto"
		}
		if err := token.Insert(); err != nil {
			return dto.FailMsg(common.TranslateMessage(ginCtx, "user.create_default_token_error"))
		}
	}

	return dto.Msg("")
}

func GetAllUsers(c fuego.ContextNoBody) (*dto.Response[dto.PageData[*model.User]], error) {
	pageInfo := dto.PageInfo(c)
	users, total, err := model.GetAllUsers(pageInfo)
	if err != nil {
		return dto.FailPage[*model.User](err.Error())
	}

	return dto.OkPage(pageInfo, users, int(total))
}

func SearchUsers(c fuego.ContextWithParams[dto.SearchUsersParams]) (*dto.Response[dto.PageData[*model.User]], error) {
	p, _ := dto.ParseParams[dto.SearchUsersParams](c)
	pageInfo := dto.PageInfo(c)
	users, total, err := model.SearchUsers(p.Keyword, p.Group, p.Role, p.Status, pageInfo.GetStartIdx(), pageInfo.GetPageSize())
	if err != nil {
		return dto.FailPage[*model.User](err.Error())
	}

	return dto.OkPage(pageInfo, users, int(total))
}

func canManageTargetRole(myRole int, targetRole int) bool {
	return myRole == common.RoleRootUser || myRole > targetRole
}

func GetUser(c fuego.ContextNoBody) (*dto.Response[model.User], error) {
	id, err := c.PathParamIntErr("id")
	if err != nil {
		return dto.Fail[model.User](err.Error())
	}
	user, err := model.GetUserById(id, false)
	if err != nil {
		return dto.Fail[model.User](err.Error())
	}
	myRole := dto.UserRole(c)
	if !canManageTargetRole(myRole, user.Role) {
		return dto.Fail[model.User](common.TranslateMessage(dto.GinCtx(c), "user.no_permission_same_level"))
	}
	user.AdminPermissions = authz.Capabilities(user.Id, user.Role)
	return dto.Ok(*user)
}

func GenerateAccessToken(c fuego.ContextNoBody) (*dto.Response[string], error) {
	id := dto.UserID(c)
	user, err := model.GetUserById(id, true)
	if err != nil {
		return dto.Fail[string](err.Error())
	}
	randI := common.GetRandomInt(4)
	key, err := common.GenerateRandomKey(29 + randI)
	if err != nil {
		common.SysLog(i18n.Translate("ctrl.failed_to_generate_key") + err.Error())
		return dto.Fail[string](common.TranslateMessage(dto.GinCtx(c), "common.generate_failed"))
	}
	user.SetAccessToken(key)

	if model.DB.Where("access_token = ?", user.AccessToken).First(user).RowsAffected != 0 {
		return dto.Fail[string](common.TranslateMessage(dto.GinCtx(c), "common.uuid_duplicate"))
	}

	if err := user.Update(false); err != nil {
		return dto.Fail[string](err.Error())
	}

	return dto.Ok(user.GetAccessToken())
}

func TransferAffQuota(c fuego.ContextWithBody[dto.TransferAffQuotaRequest]) (dto.MessageResponse, error) {
	ginCtx := dto.GinCtx(c)
	if !operation_setting.IsPaymentComplianceConfirmed() {
		return dto.FailMsg(common.TranslateMessage(ginCtx, i18n.MsgPaymentComplianceRequired))
	}
	id := dto.UserID(c)
	user, err := model.GetUserById(id, true)
	if err != nil {
		return dto.FailMsg(err.Error())
	}
	tran, err := c.Body()
	if err != nil {
		return dto.FailMsg(err.Error())
	}
	err = user.TransferAffQuotaToQuota(tran.Quota)
	if err != nil {
		return dto.FailMsg(common.TranslateMessage(dto.GinCtx(c), "user.transfer_failed", map[string]any{"Error": err.Error()}))
	}
	return dto.Msg(common.TranslateMessage(dto.GinCtx(c), "user.transfer_success"))
}

func GetAffCode(c fuego.ContextNoBody) (*dto.Response[string], error) {
	id := dto.UserID(c)
	user, err := model.GetUserById(id, true)
	if err != nil {
		return dto.Fail[string](err.Error())
	}
	if user.AffCode == "" {
		user.AffCode = common.GetRandomString(4)
		if err := user.Update(false); err != nil {
			return dto.Fail[string](err.Error())
		}
	}
	return dto.Ok(user.AffCode)
}

func GetInvitedUsers(c fuego.ContextNoBody) (*dto.Response[dto.PageData[*model.InvitedUser]], error) {
	id := dto.UserID(c)
	pageInfo := common.GetPageQuery(dto.GinCtx(c))
	users, total, err := model.GetInvitedUsers(id, pageInfo)
	if err != nil {
		return dto.FailPage[*model.InvitedUser](err.Error())
	}
	return dto.OkPage(pageInfo, users, int(total))
}

func GetReferralCommissions(c fuego.ContextNoBody) (*dto.Response[dto.PageData[*model.ReferralCommissionWithUser]], error) {
	id := dto.UserID(c)
	pageInfo := common.GetPageQuery(dto.GinCtx(c))
	commissions, total, err := model.GetUserReferralCommissions(id, pageInfo)
	if err != nil {
		return dto.FailPage[*model.ReferralCommissionWithUser](err.Error())
	}
	return dto.OkPage(pageInfo, commissions, int(total))
}

func GetSelf(c fuego.ContextNoBody) (*dto.Response[dto.UserSelfData], error) {
	id := dto.UserID(c)
	userRole := dto.UserRole(c)
	user, err := model.GetUserById(id, false)
	if err != nil {
		return dto.Fail[dto.UserSelfData](err.Error())
	}
	user.Remark = ""

	permissions := calculateUserPermissions(userRole)
	permissions["admin_permissions"] = authz.Capabilities(id, userRole)

	userSetting := user.GetSetting()

	data := dto.UserSelfData{
		Id:                        user.Id,
		Username:                  user.Username,
		DisplayName:               user.DisplayName,
		Role:                      user.Role,
		Status:                    user.Status,
		Email:                     user.Email,
		GitHubId:                  user.GitHubId,
		DiscordId:                 user.DiscordId,
		OidcId:                    user.OidcId,
		WeChatId:                  user.WeChatId,
		TelegramId:                user.TelegramId,
		Group:                     user.Group,
		Quota:                     user.Quota,
		UsedQuota:                 user.UsedQuota,
		RequestCount:              user.RequestCount,
		AffCode:                   user.AffCode,
		AffCount:                  user.AffCount,
		AffQuota:                  user.AffQuota,
		AffHistoryQuota:           user.AffHistoryQuota,
		AffCommissionRate:         effectiveCommissionRate(user.ReferralCommissionPercent),
		AffCommissionMaxRecharges: common.ReferralCommissionMaxRecharges,
		InviterId:                 user.InviterId,
		LinuxDOId:                 user.LinuxDOId,
		Setting:                   user.Setting,
		StripeCustomer:            user.StripeCustomer,
		SidebarModules:            userSetting.SidebarModules,
		Permissions:               permissions,
	}

	// Optional join: only when the user actually has per-user grants. Keeps the
	// common (no-private-group) path free of extra lookups.
	if len(userSetting.UsableGroups) > 0 {
		for _, g := range userSetting.UsableGroups {
			if g == "" || !ratio_setting.ContainsGroupRatio(g) {
				continue
			}
			data.PrivateGroups = append(data.PrivateGroups, dto.PrivateGroupInfo{
				Group:  g,
				Desc:   setting.GetUsableGroupDescription(g),
				Ratio:  ratio_setting.GetGroupRatio(g),
				Models: model.GetGroupEnabledModels(g),
			})
		}
	}

	return dto.Ok(data)
}

func effectiveCommissionRate(perUser *float64) float64 {
	if perUser != nil {
		return *perUser
	}
	return common.ReferralCommissionPercent
}

func calculateUserPermissions(userRole int) map[string]interface{} {
	permissions := map[string]interface{}{}

	if userRole == common.RoleRootUser {
		permissions["sidebar_settings"] = false
		permissions["sidebar_modules"] = map[string]interface{}{}
	} else if userRole == common.RoleAdminUser {
		permissions["sidebar_settings"] = true
		permissions["sidebar_modules"] = map[string]interface{}{
			"admin": map[string]interface{}{
				"setting": false,
			},
		}
	} else {
		permissions["sidebar_settings"] = true
		permissions["sidebar_modules"] = map[string]interface{}{
			"admin": false,
		}
	}

	return permissions
}

func generateDefaultSidebarConfig(userRole int) string {
	defaultConfig := map[string]interface{}{}

	defaultConfig["chat"] = map[string]interface{}{
		"enabled":    true,
		"playground": true,
		"chat":       true,
	}

	defaultConfig["console"] = map[string]interface{}{
		"enabled":    true,
		"detail":     true,
		"token":      true,
		"log":        true,
		"midjourney": true,
		"task":       true,
	}

	defaultConfig["personal"] = map[string]interface{}{
		"enabled":  true,
		"topup":    true,
		"personal": true,
	}

	if userRole == common.RoleAdminUser {
		defaultConfig["admin"] = map[string]interface{}{
			"enabled":    true,
			"channel":    true,
			"models":     true,
			"redemption": true,
			"user":       true,
			"setting":    false,
		}
	} else if userRole == common.RoleRootUser {
		defaultConfig["admin"] = map[string]interface{}{
			"enabled":    true,
			"channel":    true,
			"models":     true,
			"redemption": true,
			"user":       true,
			"setting":    true,
		}
	}

	configBytes, err := json.Marshal(defaultConfig)
	if err != nil {
		common.SysLog(i18n.Translate("log.default_sidebar_config_failed", map[string]any{"Error": err.Error()}))
		return ""
	}

	return string(configBytes)
}

func GetUserModels(c fuego.ContextNoBody) (*dto.Response[[]string], error) {
	id, err := c.PathParamIntErr("id")
	if err != nil {
		id = dto.UserID(c)
	}
	user, err := model.GetUserCache(id)
	if err != nil {
		return dto.Fail[[]string](err.Error())
	}
	groups := service.GetUserUsableGroups(user.Group)
	group := dto.GinCtx(c).Query("group")
	if group != "" {
		if _, ok := groups[group]; !ok {
			return dto.Ok([]string{})
		}
		return dto.Ok(model.GetGroupEnabledModels(group))
	}

	var models []string
	for group := range groups {
		for _, g := range model.GetGroupEnabledModels(group) {
			if !common.StringsContains(models, g) {
				models = append(models, g)
			}
		}
	}
	return dto.Ok(models)
}

func UpdateUser(c fuego.ContextWithBody[model.User]) (dto.MessageResponse, error) {
	ginCtx := dto.GinCtx(c)
	updatedUser, err := c.Body()
	if err != nil || updatedUser.Id == 0 {
		return dto.FailMsg(common.TranslateMessage(ginCtx, "common.invalid_params"))
	}
	// Password is optional on update: empty = keep unchanged (the `omitempty`
	// validate tag lets an empty password bind + pass validation).
	if err := common.Validate.Struct(&updatedUser); err != nil {
		return dto.FailMsg(common.TranslateMessage(ginCtx, "user.input_invalid", map[string]any{"Error": err.Error()}))
	}
	originUser, err := model.GetUserById(updatedUser.Id, false)
	if err != nil {
		return dto.FailMsg(err.Error())
	}
	myRole := dto.UserRole(c)
	if !canManageTargetRole(myRole, originUser.Role) {
		return dto.FailMsg(common.TranslateMessage(ginCtx, "user.no_permission_higher_level"))
	}
	if !canManageTargetRole(myRole, updatedUser.Role) {
		return dto.FailMsg(common.TranslateMessage(ginCtx, "user.cannot_create_higher_level"))
	}
	if updatedUser.Password == "$I_LOVE_U" {
		updatedUser.Password = "" // rollback to what it should be
	}
	updatePassword := updatedUser.Password != ""
	authzTouched := false
	if err := model.DB.Transaction(func(tx *gorm.DB) error {
		if err := updatedUser.EditWithTx(tx, updatePassword); err != nil {
			return err
		}
		touched, err := updateAdminPermissionsForUserInTx(ginCtx, tx, updatedUser.Id, originUser.Role, updatedUser.AdminPermissions)
		authzTouched = touched
		return err
	}); err != nil {
		return dto.FailMsg(err.Error())
	}
	if authzTouched {
		if err := authz.ReloadPolicy(); err != nil {
			return dto.FailMsg(err.Error())
		}
	}
	if err := model.InvalidateUserCache(updatedUser.Id); err != nil {
		common.SysLog(fmt.Sprintf("failed to invalidate user cache for user %d: %s", updatedUser.Id, err.Error()))
	}
	recordManageAuditFor(ginCtx, updatedUser.Id, "user.update", map[string]interface{}{
		"username": originUser.Username,
		"id":       updatedUser.Id,
	})
	return dto.Msg("")
}

func AdminClearUserBinding(c fuego.ContextNoBody) (dto.MessageResponse, error) {
	ginCtx := dto.GinCtx(c)
	id, err := c.PathParamIntErr("id")
	if err != nil {
		return dto.FailMsg(common.TranslateMessage(ginCtx, "common.invalid_params"))
	}

	bindingType := strings.ToLower(strings.TrimSpace(c.PathParam("binding_type")))
	if bindingType == "" {
		return dto.FailMsg(common.TranslateMessage(ginCtx, "common.invalid_params"))
	}

	user, err := model.GetUserById(id, false)
	if err != nil {
		return dto.FailMsg(err.Error())
	}

	myRole := dto.UserRole(c)
	if !canManageTargetRole(myRole, user.Role) {
		return dto.FailMsg(common.TranslateMessage(ginCtx, "user.no_permission_same_level"))
	}

	if err := user.ClearBinding(bindingType); err != nil {
		return dto.FailMsg(err.Error())
	}

	recordManageAuditFor(ginCtx, user.Id, "user.binding_clear", map[string]interface{}{
		"bindingType": bindingType,
		"username":    user.Username,
	})

	return dto.Msg("success")
}

// selfUnbindableTypes lists the OAuth binding types a user may remove from their own account.
var selfUnbindableTypes = map[string]bool{
	"github":   true,
	"discord":  true,
	"oidc":     true,
	"wechat":   true,
	"telegram": true,
	"linuxdo":  true,
}

func SelfClearBinding(c fuego.ContextNoBody) (dto.MessageResponse, error) {
	ginCtx := dto.GinCtx(c)
	userId := dto.UserID(c)
	if userId == 0 {
		return dto.FailMsg(common.TranslateMessage(ginCtx, "common.not_logged_in"))
	}

	bindingType := strings.ToLower(strings.TrimSpace(c.PathParam("binding_type")))
	if !selfUnbindableTypes[bindingType] {
		return dto.FailMsg(common.TranslateMessage(ginCtx, "common.invalid_params"))
	}

	user, err := model.GetUserById(userId, false)
	if err != nil {
		return dto.FailMsg(err.Error())
	}

	if err := user.ClearBinding(bindingType); err != nil {
		return dto.FailMsg(err.Error())
	}

	return dto.Msg(common.TranslateMessage(ginCtx, "custom_oauth.unbind_success"))
}

func UpdateSelf(c fuego.ContextNoBody) (dto.MessageResponse, error) {
	ginCtx := dto.GinCtx(c)
	var requestData map[string]interface{}
	err := dto.Decode(c, &requestData)
	if err != nil {
		return dto.FailMsg(common.TranslateMessage(ginCtx, "common.invalid_params"))
	}

	if sidebarModules, sidebarExists := requestData["sidebar_modules"]; sidebarExists {
		userId := dto.UserID(c)
		user, err := model.GetUserById(userId, false)
		if err != nil {
			return dto.FailMsg(err.Error())
		}

		currentSetting := user.GetSetting()

		if sidebarModulesStr, ok := sidebarModules.(string); ok {
			currentSetting.SidebarModules = sidebarModulesStr
		}

		user.SetSetting(currentSetting)
		if err := user.Update(false); err != nil {
			return dto.FailMsg(common.TranslateMessage(ginCtx, "common.update_failed"))
		}

		return dto.Msg(common.TranslateMessage(ginCtx, "common.update_success"))
	}

	if language, langExists := requestData["language"]; langExists {
		userId := dto.UserID(c)
		user, err := model.GetUserById(userId, false)
		if err != nil {
			return dto.FailMsg(err.Error())
		}

		currentSetting := user.GetSetting()

		if langStr, ok := language.(string); ok {
			currentSetting.Language = langStr
		}

		user.SetSetting(currentSetting)
		if err := user.Update(false); err != nil {
			return dto.FailMsg(common.TranslateMessage(ginCtx, "common.update_failed"))
		}

		return dto.Msg(common.TranslateMessage(ginCtx, "common.update_success"))
	}

	var user model.User
	requestDataBytes, err := json.Marshal(requestData)
	if err != nil {
		return dto.FailMsg(common.TranslateMessage(ginCtx, "common.invalid_params"))
	}
	err = json.Unmarshal(requestDataBytes, &user)
	if err != nil {
		return dto.FailMsg(common.TranslateMessage(ginCtx, "common.invalid_params"))
	}

	if user.Password == "" {
		user.Password = "$I_LOVE_U"
	}
	if err := common.Validate.Struct(&user); err != nil {
		return dto.FailMsg(common.TranslateMessage(ginCtx, "common.invalid_input"))
	}

	cleanUser := model.User{
		Id:          dto.UserID(c),
		Username:    user.Username,
		Password:    user.Password,
		DisplayName: user.DisplayName,
	}
	if user.Password == "$I_LOVE_U" {
		user.Password = ""
		cleanUser.Password = ""
	}
	updatePassword, err := checkUpdatePassword(ginCtx, user.OriginalPassword, user.Password, cleanUser.Id)
	if err != nil {
		return dto.FailMsg(err.Error())
	}
	if err := cleanUser.Update(updatePassword); err != nil {
		return dto.FailMsg(err.Error())
	}

	return dto.Msg("")
}

func checkUpdatePassword(ginCtx *gin.Context, originalPassword string, newPassword string, userId int) (updatePassword bool, err error) {
	var currentUser *model.User
	currentUser, err = model.GetUserById(userId, true)
	if err != nil {
		return
	}

	if !common.ValidatePasswordAndHash(originalPassword, currentUser.Password) && currentUser.Password != "" {
		err = fmt.Errorf("%s", common.TranslateMessage(ginCtx, "user.original_password_error"))
		return
	}
	if newPassword == "" {
		return
	}
	updatePassword = true
	return
}

func DeleteUser(c fuego.ContextNoBody) (dto.MessageResponse, error) {
	id, err := c.PathParamIntErr("id")
	if err != nil {
		return dto.FailMsg(err.Error())
	}
	originUser, err := model.GetUserByIdUnscoped(id)
	if err != nil {
		return dto.FailMsg(err.Error())
	}
	myRole := dto.UserRole(c)
	if myRole <= originUser.Role {
		return dto.FailMsg(common.TranslateMessage(dto.GinCtx(c), "user.no_permission_higher_level"))
	}
	err = model.HardDeleteUserById(id)
	if err != nil {
		return dto.FailMsg(err.Error())
	}
	recordManageAuditFor(dto.GinCtx(c), originUser.Id, "user.delete", map[string]interface{}{
		"username": originUser.Username,
		"id":       originUser.Id,
	})
	return dto.Msg("")
}

func DeleteSelf(c fuego.ContextNoBody) (dto.MessageResponse, error) {
	id := dto.UserID(c)
	user, _ := model.GetUserById(id, false)

	if user.Role == common.RoleRootUser {
		return dto.FailMsg(common.TranslateMessage(dto.GinCtx(c), "user.cannot_delete_root_user"))
	}

	err := model.DeleteUserById(id)
	if err != nil {
		return dto.FailMsg(err.Error())
	}
	return dto.Msg("")
}

func CreateUser(c fuego.ContextWithBody[model.User]) (dto.MessageResponse, error) {
	ginCtx := dto.GinCtx(c)
	user, err := c.Body()
	user.Username = strings.TrimSpace(user.Username)
	if err != nil || user.Username == "" || user.Password == "" {
		return dto.FailMsg(common.TranslateMessage(ginCtx, "common.invalid_params"))
	}
	if err := common.Validate.Struct(&user); err != nil {
		return dto.FailMsg(common.TranslateMessage(ginCtx, "user.input_invalid", map[string]any{"Error": err.Error()}))
	}
	if user.DisplayName == "" {
		user.DisplayName = user.Username
	}
	myRole := dto.UserRole(c)
	if user.Role >= myRole {
		return dto.FailMsg(common.TranslateMessage(ginCtx, "user.cannot_create_higher_level"))
	}
	cleanUser := model.User{
		Username:    user.Username,
		Password:    user.Password,
		DisplayName: user.DisplayName,
		Role:        user.Role,
	}
	authzTouched := false
	if err := model.DB.Transaction(func(tx *gorm.DB) error {
		if err := cleanUser.InsertWithTx(tx, 0); err != nil {
			return err
		}
		touched, err := updateAdminPermissionsForUserInTx(ginCtx, tx, cleanUser.Id, cleanUser.Role, user.AdminPermissions)
		authzTouched = touched
		return err
	}); err != nil {
		return dto.FailMsg(err.Error())
	}
	if authzTouched {
		if err := authz.ReloadPolicy(); err != nil {
			return dto.FailMsg(err.Error())
		}
	}
	cleanUser.FinishInsert(0)

	recordManageAuditFor(ginCtx, cleanUser.Id, "user.create", map[string]interface{}{
		"username": cleanUser.Username,
		"role":     cleanUser.Role,
	})
	return dto.Msg("")
}

func updateAdminPermissionsForUserInTx(c *gin.Context, tx *gorm.DB, userID int, userRole int, permissions map[string]map[string]bool) (bool, error) {
	if permissions == nil {
		if userRole < common.RoleAdminUser && c.GetInt("role") == common.RoleRootUser {
			return true, authz.ClearUserAuthorizationInTx(tx, userID)
		}
		return false, nil
	}
	if c.GetInt("role") != common.RoleRootUser {
		return false, fmt.Errorf("only root can update admin permissions")
	}
	if userRole < common.RoleAdminUser {
		return true, authz.ClearUserAuthorizationInTx(tx, userID)
	}
	return true, authz.SetUserPermissionsInTx(tx, userID, permissions)
}

// ManageUser handles user management actions (enable/disable/delete/promote/demote)
func ManageUser(c fuego.ContextWithBody[dto.ManageRequest]) (*dto.Response[dto.ManageUserData], error) {
	ginCtx := dto.GinCtx(c)
	req, err := c.Body()

	if err != nil {
		return dto.Fail[dto.ManageUserData](common.TranslateMessage(ginCtx, "common.invalid_params"))
	}
	user := model.User{
		Id: req.Id,
	}
	model.DB.Unscoped().Where(&user).First(&user)
	if user.Id == 0 {
		return dto.Fail[dto.ManageUserData](common.TranslateMessage(ginCtx, "user.not_exists"))
	}
	myRole := dto.UserRole(c)
	if !canManageTargetRole(myRole, user.Role) {
		return dto.Fail[dto.ManageUserData](common.TranslateMessage(ginCtx, "user.no_permission_higher_level"))
	}
	switch req.Action {
	case "disable":
		user.Status = common.UserStatusDisabled
		if user.Role == common.RoleRootUser {
			return dto.Fail[dto.ManageUserData](common.TranslateMessage(ginCtx, "user.cannot_disable_root_user"))
		}
	case "enable":
		user.Status = common.UserStatusEnabled
	case "delete":
		if user.Role == common.RoleRootUser {
			return dto.Fail[dto.ManageUserData](common.TranslateMessage(ginCtx, "user.cannot_delete_root_user"))
		}
		if err := user.Delete(); err != nil {
			return dto.Fail[dto.ManageUserData](err.Error())
		}
	case "promote":
		if myRole != common.RoleRootUser {
			return dto.Fail[dto.ManageUserData](common.TranslateMessage(ginCtx, "user.admin_cannot_promote"))
		}
		if user.Role >= common.RoleAdminUser {
			return dto.Fail[dto.ManageUserData](common.TranslateMessage(ginCtx, "user.already_admin"))
		}
		user.Role = common.RoleAdminUser
	case "demote":
		if user.Role == common.RoleRootUser {
			return dto.Fail[dto.ManageUserData](common.TranslateMessage(ginCtx, "user.cannot_demote_root_user"))
		}
		if user.Role == common.RoleCommonUser {
			return dto.Fail[dto.ManageUserData](common.TranslateMessage(ginCtx, "user.already_common"))
		}
		user.Role = common.RoleCommonUser
	case "add_quota":
		switch req.Mode {
		case "add":
			if req.Value <= 0 {
				return dto.Fail[dto.ManageUserData](common.TranslateMessage(ginCtx, i18n.MsgUserQuotaChangeZero))
			}
			if err := model.IncreaseUserQuota(user.Id, req.Value, true); err != nil {
				return dto.Fail[dto.ManageUserData](err.Error())
			}
			recordManageAuditFor(ginCtx, user.Id, "user.quota_add", map[string]interface{}{
				"quota": logger.LogQuota(req.Value),
			})
		case "subtract":
			if req.Value <= 0 {
				return dto.Fail[dto.ManageUserData](common.TranslateMessage(ginCtx, i18n.MsgUserQuotaChangeZero))
			}
			if err := model.DecreaseUserQuota(user.Id, req.Value, true); err != nil {
				return dto.Fail[dto.ManageUserData](err.Error())
			}
			recordManageAuditFor(ginCtx, user.Id, "user.quota_subtract", map[string]interface{}{
				"quota": logger.LogQuota(req.Value),
			})
		case "override":
			oldQuota := user.Quota
			if err := model.DB.Model(&model.User{}).Where("id = ?", user.Id).Update("quota", req.Value).Error; err != nil {
				return dto.Fail[dto.ManageUserData](err.Error())
			}
			recordManageAuditFor(ginCtx, user.Id, "user.quota_override", map[string]interface{}{
				"from": logger.LogQuota(oldQuota),
				"to":   logger.LogQuota(req.Value),
			})
		default:
			return dto.Fail[dto.ManageUserData](common.TranslateMessage(ginCtx, i18n.MsgInvalidParams))
		}
		return dto.Ok(dto.ManageUserData{Role: user.Role, Status: user.Status})
	case "set_block_free":
		s := user.GetSetting()
		s.BlockFreeWhenNoQuota = req.Value == 1
		user.SetSetting(s)
		if err := user.Update(false); err != nil {
			return dto.Fail[dto.ManageUserData](err.Error())
		}
		adminName := ginCtx.GetString("username")
		model.RecordLog(user.Id, model.LogTypeManage,
			i18n.T(ginCtx, "ctrl.admin_set_block_free", map[string]any{"Admin": adminName, "Enabled": req.Value == 1}))
		return dto.Ok(dto.ManageUserData{Role: user.Role, Status: user.Status})
	case "set_usable_groups":
		// Per-user usable-group grants (private routing groups). Keep only
		// non-empty groups that exist in GroupRatio so we never store junk.
		seen := make(map[string]bool)
		groups := make([]string, 0, len(req.Groups))
		for _, g := range req.Groups {
			if g == "" || seen[g] || !ratio_setting.ContainsGroupRatio(g) {
				continue
			}
			seen[g] = true
			groups = append(groups, g)
		}
		s := user.GetSetting()
		s.UsableGroups = groups
		user.SetSetting(s)
		if err := user.Update(false); err != nil {
			return dto.Fail[dto.ManageUserData](err.Error())
		}
		adminName := ginCtx.GetString("username")
		model.RecordLog(user.Id, model.LogTypeManage,
			i18n.T(ginCtx, "ctrl.admin_set_usable_groups", map[string]any{"Admin": adminName, "Groups": strings.Join(groups, ", ")}))
		return dto.Ok(dto.ManageUserData{Role: user.Role, Status: user.Status})
	}

	authzTouched := false
	if req.Action == "demote" {
		if err := model.DB.Transaction(func(tx *gorm.DB) error {
			if err := user.UpdateWithTx(tx, false); err != nil {
				return err
			}
			authzTouched = true
			return authz.ClearUserAuthorizationInTx(tx, user.Id)
		}); err != nil {
			return dto.Fail[dto.ManageUserData](err.Error())
		}
		if authzTouched {
			if err := authz.ReloadPolicy(); err != nil {
				return dto.Fail[dto.ManageUserData](err.Error())
			}
		}
	} else {
		if err := user.Update(false); err != nil {
			return dto.Fail[dto.ManageUserData](err.Error())
		}
	}
	// 禁用 / 角色调整后，强制失效用户缓存与其全部令牌缓存，
	// 避免在 Redis TTL 过期前仍使用旧状态（尤其是禁用后仍可发起请求的问题）。
	// InvalidateUserCache 会让下一次 GetUserCache 从数据库重新加载，
	// InvalidateUserTokensCache 则确保令牌侧的缓存也同步刷新。
	if req.Action == "disable" || req.Action == "promote" || req.Action == "demote" {
		if err := model.InvalidateUserCache(user.Id); err != nil {
			common.SysLog(fmt.Sprintf("failed to invalidate user cache for user %d: %s", user.Id, err.Error()))
		}
		if err := model.InvalidateUserTokensCache(user.Id); err != nil {
			common.SysLog(fmt.Sprintf("failed to invalidate tokens cache for user %d: %s", user.Id, err.Error()))
		}
	}
	recordManageAuditFor(ginCtx, user.Id, "user.manage", map[string]interface{}{
		"action":   req.Action,
		"username": user.Username,
		"id":       user.Id,
	})
	return dto.Ok(dto.ManageUserData{Role: user.Role, Status: user.Status})
}

// GrantDiscordQuota grants quota to the user linked to a Discord ID.
// Repeatable: it always adds quota when the Discord account is linked. The caller
// (the Discord bot) owns any idempotency/audit. Returns Linked=false when no user
// has bound the given Discord ID.
func GrantDiscordQuota(c fuego.ContextWithBody[dto.GrantDiscordQuotaRequest]) (*dto.Response[dto.GrantDiscordQuotaData], error) {
	ginCtx := dto.GinCtx(c)
	req, err := c.Body()
	if err != nil {
		return dto.Fail[dto.GrantDiscordQuotaData](common.TranslateMessage(ginCtx, "common.invalid_params"))
	}
	if req.DiscordId == "" || req.Quota <= 0 {
		return dto.Fail[dto.GrantDiscordQuotaData](common.TranslateMessage(ginCtx, "common.invalid_params"))
	}

	if !model.IsDiscordIdAlreadyTaken(req.DiscordId) {
		return dto.Ok(dto.GrantDiscordQuotaData{Linked: false})
	}

	user := model.User{DiscordId: req.DiscordId}
	if err := user.FillUserByDiscordId(); err != nil {
		return dto.Fail[dto.GrantDiscordQuotaData](err.Error())
	}
	if user.Id == 0 {
		return dto.Ok(dto.GrantDiscordQuotaData{Linked: false})
	}

	if err := model.IncreaseUserQuota(user.Id, req.Quota, true); err != nil {
		return dto.Fail[dto.GrantDiscordQuotaData](err.Error())
	}

	adminName := ginCtx.GetString("username")
	model.RecordLog(user.Id, model.LogTypeManage,
		i18n.T(ginCtx, "ctrl.discord_grant_quota", map[string]any{"Admin": adminName, "Quota": logger.LogQuota(req.Quota)}))

	return dto.Ok(dto.GrantDiscordQuotaData{UserId: user.Id, Linked: true})
}

func EmailBind(c fuego.ContextWithParams[dto.EmailBindParams]) (dto.MessageResponse, error) {
	ginCtx := dto.GinCtx(c)
	p, _ := dto.ParseParams[dto.EmailBindParams](c)
	if !common.VerifyCodeWithKey(p.Email, p.Code, common.EmailVerificationPurpose) {
		return dto.FailMsg(common.TranslateMessage(ginCtx, "user.verification_code_error"))
	}
	session := sessions.Default(ginCtx)
	id, ok := session.Get("id").(int)
	if !ok || id == 0 {
		return dto.FailMsg(common.TranslateMessage(ginCtx, "common.not_logged_in"))
	}
	user := model.User{
		Id: id,
	}
	if err := user.FillUserById(); err != nil {
		return dto.FailMsg(err.Error())
	}
	user.Email = p.Email
	if err := user.Update(false); err != nil {
		return dto.FailMsg(err.Error())
	}
	return dto.Msg("")
}

var topUpLocks sync.Map
var topUpCreateLock sync.Mutex

type topUpTryLock struct {
	ch chan struct{}
}

func newTopUpTryLock() *topUpTryLock {
	return &topUpTryLock{ch: make(chan struct{}, 1)}
}

func (l *topUpTryLock) TryLock() bool {
	select {
	case l.ch <- struct{}{}:
		return true
	default:
		return false
	}
}

func (l *topUpTryLock) Unlock() {
	select {
	case <-l.ch:
	default:
	}
}

func getTopUpLock(userID int) *topUpTryLock {
	if v, ok := topUpLocks.Load(userID); ok {
		return v.(*topUpTryLock)
	}
	topUpCreateLock.Lock()
	defer topUpCreateLock.Unlock()
	if v, ok := topUpLocks.Load(userID); ok {
		return v.(*topUpTryLock)
	}
	l := newTopUpTryLock()
	topUpLocks.Store(userID, l)
	return l
}

func TopUp(c fuego.ContextWithBody[dto.TopUpRequest]) (*dto.Response[int], error) {
	ginCtx := dto.GinCtx(c)
	if !operation_setting.IsPaymentComplianceConfirmed() {
		return dto.Fail[int](common.TranslateMessage(ginCtx, i18n.MsgPaymentComplianceRequired))
	}
	id := dto.UserID(c)
	lock := getTopUpLock(id)
	if !lock.TryLock() {
		return dto.Fail[int](common.TranslateMessage(ginCtx, "user.topup_processing"))
	}
	defer lock.Unlock()
	req, err := c.Body()
	if err != nil {
		return dto.Fail[int](err.Error())
	}
	quota, err := model.Redeem(req.Key, id)
	if err != nil {
		if errors.Is(err, model.ErrRedeemFailed) {
			return dto.Fail[int](common.TranslateMessage(ginCtx, "redeem.failed"))
		}
		return dto.Fail[int](err.Error())
	}
	return dto.Ok(quota)
}

func UpdateUserSetting(c fuego.ContextWithBody[dto.UpdateUserSettingRequest]) (dto.MessageResponse, error) {
	ginCtx := dto.GinCtx(c)
	req, err := c.Body()
	if err != nil {
		return dto.FailMsg(common.TranslateMessage(ginCtx, "common.invalid_params"))
	}

	if req.QuotaWarningType != dto.NotifyTypeEmail && req.QuotaWarningType != dto.NotifyTypeWebhook && req.QuotaWarningType != dto.NotifyTypeBark && req.QuotaWarningType != dto.NotifyTypeGotify {
		return dto.FailMsg(common.TranslateMessage(ginCtx, "setting.invalid_type"))
	}

	if req.QuotaWarningThreshold <= 0 {
		return dto.FailMsg(common.TranslateMessage(ginCtx, "quota.threshold_gt_zero"))
	}

	if req.QuotaWarningType == dto.NotifyTypeWebhook {
		if req.WebhookUrl == "" {
			return dto.FailMsg(common.TranslateMessage(ginCtx, "setting.webhook_empty"))
		}
		if _, err := url.ParseRequestURI(req.WebhookUrl); err != nil {
			return dto.FailMsg(common.TranslateMessage(ginCtx, "setting.webhook_invalid"))
		}
	}

	if req.QuotaWarningType == dto.NotifyTypeEmail && req.NotificationEmail != "" {
		if !strings.Contains(req.NotificationEmail, "@") {
			return dto.FailMsg(common.TranslateMessage(ginCtx, "setting.email_invalid"))
		}
	}

	if req.QuotaWarningType == dto.NotifyTypeBark {
		if req.BarkUrl == "" {
			return dto.FailMsg(common.TranslateMessage(ginCtx, "setting.bark_url_empty"))
		}
		if _, err := url.ParseRequestURI(req.BarkUrl); err != nil {
			return dto.FailMsg(common.TranslateMessage(ginCtx, "setting.bark_url_invalid"))
		}
		if !strings.HasPrefix(req.BarkUrl, "https://") && !strings.HasPrefix(req.BarkUrl, "http://") {
			return dto.FailMsg(common.TranslateMessage(ginCtx, "setting.url_must_http"))
		}
	}

	if req.QuotaWarningType == dto.NotifyTypeGotify {
		if req.GotifyUrl == "" {
			return dto.FailMsg(common.TranslateMessage(ginCtx, "setting.gotify_url_empty"))
		}
		if req.GotifyToken == "" {
			return dto.FailMsg(common.TranslateMessage(ginCtx, "setting.gotify_token_empty"))
		}
		if _, err := url.ParseRequestURI(req.GotifyUrl); err != nil {
			return dto.FailMsg(common.TranslateMessage(ginCtx, "setting.gotify_url_invalid"))
		}
		if !strings.HasPrefix(req.GotifyUrl, "https://") && !strings.HasPrefix(req.GotifyUrl, "http://") {
			return dto.FailMsg(common.TranslateMessage(ginCtx, "setting.url_must_http"))
		}
	}

	userId := dto.UserID(c)
	user, err := model.GetUserById(userId, true)
	if err != nil {
		return dto.FailMsg(err.Error())
	}
	existingSettings := user.GetSetting()
	upstreamModelUpdateNotifyEnabled := existingSettings.UpstreamModelUpdateNotifyEnabled
	if user.Role >= common.RoleAdminUser && req.UpstreamModelUpdateNotifyEnabled != nil {
		upstreamModelUpdateNotifyEnabled = *req.UpstreamModelUpdateNotifyEnabled
	}

	settings := dto.UserSetting{
		NotifyType:                       req.QuotaWarningType,
		QuotaWarningThreshold:            req.QuotaWarningThreshold,
		UpstreamModelUpdateNotifyEnabled: upstreamModelUpdateNotifyEnabled,
		AcceptUnsetRatioModel:            req.AcceptUnsetModelRatioModel,
		RecordIpLog:                      req.RecordIpLog,
	}

	if req.QuotaWarningType == dto.NotifyTypeWebhook {
		settings.WebhookUrl = req.WebhookUrl
		if req.WebhookSecret != "" {
			settings.WebhookSecret = req.WebhookSecret
		}
	}

	if req.QuotaWarningType == dto.NotifyTypeEmail && req.NotificationEmail != "" {
		settings.NotificationEmail = req.NotificationEmail
	}

	if req.QuotaWarningType == dto.NotifyTypeBark {
		settings.BarkUrl = req.BarkUrl
	}

	if req.QuotaWarningType == dto.NotifyTypeGotify {
		settings.GotifyUrl = req.GotifyUrl
		settings.GotifyToken = req.GotifyToken
		if req.GotifyPriority < 0 || req.GotifyPriority > 10 {
			settings.GotifyPriority = 5
		} else {
			settings.GotifyPriority = req.GotifyPriority
		}
	}

	user.SetSetting(settings)
	if err := user.Update(false); err != nil {
		return dto.FailMsg(common.TranslateMessage(ginCtx, "common.update_failed"))
	}

	return dto.Msg(common.TranslateMessage(ginCtx, "setting.saved"))
}
