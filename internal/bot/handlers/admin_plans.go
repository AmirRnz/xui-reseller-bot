package handlers

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"gopkg.in/telebot.v3"
	"xui-end-bot/internal/bot"
	"xui-end-bot/internal/db"
	"xui-end-bot/internal/xui"
)

func RegisterAdminPlans(b *telebot.Bot, auth telebot.MiddlewareFunc, admin telebot.MiddlewareFunc) {
	b.Handle("\fadmin_plans", HandleAdminPlans, auth, admin)
	b.Handle("\fadmin_refresh_inbounds", HandleAdminRefreshInbounds, auth, admin)
	b.Handle("\fadmin_create_test_plan", HandleCreateTestPlan, auth, admin)
	b.Handle("\fadmin_create_paid_plan", HandleCreatePaidPlan, auth, admin)
	b.Handle("\fadmin_view_plan", HandleAdminViewPlan, auth, admin)
	b.Handle("\fadmin_plan_toggle", HandleAdminPlanToggle, auth, admin)
	b.Handle("\fadmin_plan_delete", HandleAdminPlanDelete, auth, admin)
	b.Handle("\fadmin_plan_access", HandleAdminPlanAccessPrompt, auth, admin)
	b.Handle("\fadmin_plan_edit", HandleAdminPlanEdit, auth, admin)
	b.Handle("\fadmin_draft_edit", HandleAdminDraftEdit, auth, admin)
	b.Handle("\fadmin_draft_inbounds", HandleAdminDraftInboundsMenu, auth, admin)
	b.Handle("\fadmin_draft_toggle_inbound", HandleAdminDraftToggleInbound, auth, admin)
	b.Handle("\fadmin_draft_toggle_all", HandleAdminDraftToggleAll, auth, admin)
	b.Handle("\fadmin_draft_inbounds_done", HandleAdminDraftInboundsDone, auth, admin)
	b.Handle("\fadmin_draft_toggle_sync", HandleAdminDraftToggleSync, auth, admin)
	b.Handle("\fadmin_draft_toggle_limited", HandleAdminDraftToggleLimited, auth, admin)
	b.Handle("\fadmin_draft_set_flow", HandleAdminDraftSetFlow, auth, admin)
	b.Handle("\fadmin_draft_action", HandleAdminDraftAction, auth, admin)
}

func HandleAdminPlans(c telebot.Context) error {
	testPlans, _ := db.GetTestPlans(context.Background(), true)
	paidPlans, _ := db.GetPaidPlans(context.Background(), true)

	inboundNames := cachedInboundNames()

	var text strings.Builder
	text.WriteString("📋 **Plans**\n\n")
	text.WriteString("🧪 Test plans:\n")
	for _, p := range testPlans {
		enabledMark := "✅"
		if !p.Enabled {
			enabledMark = "🔴"
		}
		text.WriteString(fmt.Sprintf("%s #%d %s (max %d/day, %s)\n",
			enabledMark, p.ID, p.Name, p.MaxPerDay, humanDuration(p.ExpireSeconds)))
	}
	text.WriteString("\n💼 Paid plans:\n")
	for _, p := range paidPlans {
		enabledMark := "✅"
		if !p.Enabled {
			enabledMark = "🔴"
		}
		text.WriteString(fmt.Sprintf("%s #%d %s (base %.0f, %d-%d IP)\n",
			enabledMark, p.ID, p.Name, p.BasePrice, p.BaseIPLimit, p.MaxIPLimit))
	}

	if bot.XUIClient != nil {
		text.WriteString("\n📡 Cached inbounds:\n")
		for _, inbound := range bot.XUIClient.GetCachedInbounds() {
			inboundNames[inbound.ID] = inbound.Remark
			text.WriteString(fmt.Sprintf("  ID %d: %s (%s:%d)\n", inbound.ID, inbound.Remark, inbound.Protocol, inbound.Port))
		}
	}

	menu := &telebot.ReplyMarkup{}
	var rows []telebot.Row
	for _, p := range testPlans {
		rows = append(rows, menu.Row(menu.Data(fmt.Sprintf("🧪 #%d %s", p.ID, p.Name), "admin_view_plan", fmt.Sprintf("test:%d", p.ID))))
	}
	for _, p := range paidPlans {
		rows = append(rows, menu.Row(menu.Data(fmt.Sprintf("💼 #%d %s", p.ID, p.Name), "admin_view_plan", fmt.Sprintf("paid:%d", p.ID))))
	}
	rows = append(rows, menu.Row(
		menu.Data("➕ Test plan", "admin_create_test_plan"),
		menu.Data("➕ Paid plan", "admin_create_paid_plan"),
	))
	rows = append(rows, menu.Row(
		menu.Data("🔄 Refresh Inbounds", "admin_refresh_inbounds"),
	))
	rows = append(rows, menu.Row(menu.Data("« Back", "admin_menu")))
	menu.Inline(rows...)
	return maybeEditOrSend(c, text.String(), menu)
}

func HandleAdminRefreshInbounds(c telebot.Context) error {
	if bot.XUIClient != nil && bot.XUIClient.Cache != nil {
		bot.XUIClient.Cache.RefreshSync()
	}
	_ = c.Respond(&telebot.CallbackResponse{Text: "🔄 Inbounds cache refreshed!"})
	return HandleAdminPlans(c)
}

func HandleCreateTestPlan(c telebot.Context) error {
	user := userFromContext(c)
	if user == nil || !isConfiguredAdmin(user.TelegramID) {
		return c.Send("Permission denied.")
	}

	draft := map[string]interface{}{
		"id":                int64(0),
		"type":              "test",
		"name":              "",
		"description":       "",
		"usage_description": "",
		"inbound_ids":       []int{},
		"expire_seconds":    int64(3600),
		"max_data_bytes":    int64(0),
		"flow":              "",
		"max_per_day":       1,
		"is_global":         true,
		"allowed_user_ids":  []int64{},
		"sync_subs":         true,
	}

	bot.FSM.SetState(user.TelegramID, "awaiting_admin_test_plan_menu", draft)
	return showAdminDraftTestPlanMenu(c, draft)
}

func HandleCreatePaidPlan(c telebot.Context) error {
	user := userFromContext(c)
	if user == nil || !isConfiguredAdmin(user.TelegramID) {
		return c.Send("Permission denied.")
	}

	draft := map[string]interface{}{
		"id":                    int64(0),
		"type":                  "paid",
		"name":                  "",
		"description":           "",
		"usage_description":     "",
		"inbound_ids":           []int{},
		"base_price":            0.0,
		"base_ip_limit":         1,
		"max_ip_limit":          1,
		"price_per_extra_ip":    0.0,
		"flow":                  "",
		"discount_tiers":        []db.DiscountTier{},
		"is_global":             true,
		"allowed_user_ids":      []int64{},
		"sync_subs":             true,
		"is_limited":            false,
		"price_per_gb":          0.0,
		"min_data_gb":           int64(0),
		"price_per_extra_month": 0.0,
	}

	bot.FSM.SetState(user.TelegramID, "awaiting_admin_paid_plan_menu", draft)
	return showAdminDraftPaidPlanMenu(c, draft)
}

func HandleAdminPlanEdit(c telebot.Context) error {
	planType, planID, ok := parsePlanRef(callbackPayload(c))
	if !ok {
		return c.Send("Invalid plan.")
	}

	user := userFromContext(c)
	if user == nil || !isConfiguredAdmin(user.TelegramID) {
		return c.Send("Permission denied.")
	}

	ctx := context.Background()
	allowedUserIDs, _ := db.GetPlanUserAccess(ctx, planType, planID)

	if planType == db.PlanTypeTest {
		plan, err := db.GetTestPlanByID(ctx, planID)
		if err != nil || plan == nil {
			return c.Send("Test plan not found.")
		}

		draft := map[string]interface{}{
			"id":                plan.ID,
			"type":              "test",
			"name":              plan.Name,
			"description":       plan.Description,
			"usage_description": plan.UsageDescription,
			"inbound_ids":       plan.InboundIDs,
			"expire_seconds":    plan.ExpireSeconds,
			"max_data_bytes":    plan.MaxDataBytes,
			"flow":              plan.Flow,
			"max_per_day":       plan.MaxPerDay,
			"is_global":         plan.IsGlobal,
			"allowed_user_ids":  allowedUserIDs,
			"sync_subs":         plan.SyncSubs,
		}

		bot.FSM.SetState(user.TelegramID, "awaiting_admin_test_plan_menu", draft)
		return showAdminDraftTestPlanMenu(c, draft)
	} else {
		plan, err := db.GetPaidPlanByID(ctx, planID)
		if err != nil || plan == nil {
			return c.Send("Paid plan not found.")
		}

		draft := map[string]interface{}{
			"id":                    plan.ID,
			"type":                  "paid",
			"name":                  plan.Name,
			"description":           plan.Description,
			"usage_description":     plan.UsageDescription,
			"inbound_ids":           plan.InboundIDs,
			"base_price":            plan.BasePrice,
			"base_ip_limit":         plan.BaseIPLimit,
			"max_ip_limit":          plan.MaxIPLimit,
			"price_per_extra_ip":    plan.PricePerExtraIP,
			"flow":                  plan.Flow,
			"discount_tiers":        plan.DiscountTiers,
			"is_global":             plan.IsGlobal,
			"allowed_user_ids":      allowedUserIDs,
			"sync_subs":             plan.SyncSubs,
			"is_limited":            plan.IsLimited,
			"price_per_gb":          plan.PricePerGB,
			"min_data_gb":           plan.MinDataGB,
			"price_per_extra_month": plan.PricePerExtraMonth,
		}

		bot.FSM.SetState(user.TelegramID, "awaiting_admin_paid_plan_menu", draft)
		return showAdminDraftPaidPlanMenu(c, draft)
	}
}

func showAdminDraftTestPlanMenu(c telebot.Context, draft map[string]interface{}) error {
	name := draftGetString(draft, "name")
	description := draftGetString(draft, "description")
	usageDescription := draftGetString(draft, "usage_description")
	inboundIDs := draftGetIntSlice(draft, "inbound_ids")
	expireSeconds := draftGetInt64(draft, "expire_seconds")
	maxDataBytes := draftGetInt64(draft, "max_data_bytes")
	flow := draftGetString(draft, "flow")
	maxPerDay := draftGetInt(draft, "max_per_day")
	isGlobal := draftGetBool(draft, "is_global")
	syncSubs := draftGetBool(draft, "sync_subs")
	allowedUserIDs := draftGetInt64Slice(draft, "allowed_user_ids")

	inboundLabel := formatInboundLabel(inboundIDs)
	durationLabel := humanDuration(expireSeconds)
	dataLabel := "unlimited"
	if maxDataBytes > 0 {
		dataLabel = fmt.Sprintf("%.2f GB", float64(maxDataBytes)/1073741824)
	}

	text := fmt.Sprintf("🧪 **Draft Test Plan Config**\n\n"+
		"📝 Name: %s\n"+
		"📝 Description: %s\n"+
		"📝 Usage Notes: %s\n"+
		"📡 Inbounds: %s\n"+
		"⏱️ Duration: %s\n"+
		"💾 Max Data: %s\n"+
		"⚡ Flow: %s\n"+
		"📊 Max per day: %d\n"+
		"👥 Access: %s\n"+
		"🔄 Sync Active Subscribers: %t\n",
		nonEmpty(name, "(not set)"),
		nonEmpty(description, "(not set)"),
		nonEmpty(usageDescription, "(not set)"),
		inboundLabel,
		durationLabel,
		dataLabel,
		nonEmpty(flow, "(default/none)"),
		maxPerDay,
		formatAccessLabel(isGlobal, allowedUserIDs),
		syncSubs)

	menu := &telebot.ReplyMarkup{}
	menu.Inline(
		menu.Row(menu.Data("📝 Name", "admin_draft_edit", "test:name"), menu.Data("📝 Description", "admin_draft_edit", "test:description"), menu.Data("📝 Usage Notes", "admin_draft_edit", "test:usage_description")),
		menu.Row(menu.Data("📡 Inbound IDs", "admin_draft_inbounds", "test"), menu.Data("⏱️ Duration", "admin_draft_edit", "test:duration")),
		menu.Row(menu.Data("💾 Max Data", "admin_draft_edit", "test:max_data"), menu.Data("⚡ Flow", "admin_draft_edit", "test:flow")),
		menu.Row(menu.Data("📊 Max/Day", "admin_draft_edit", "test:max_per_day"), menu.Data("👥 Access", "admin_draft_edit", "test:access")),
		menu.Row(menu.Data("🔄 Sync Subs: "+toggleEmoji(syncSubs), "admin_draft_toggle_sync", "test")),
		menu.Row(menu.Data("💾 Save", "admin_draft_action", "test:save"), menu.Data("❌ Cancel", "admin_draft_action", "test:cancel")),
	)
	return maybeEditOrSend(c, text, menu)
}

func showAdminDraftPaidPlanMenu(c telebot.Context, draft map[string]interface{}) error {
	name := draftGetString(draft, "name")
	description := draftGetString(draft, "description")
	usageDescription := draftGetString(draft, "usage_description")
	inboundIDs := draftGetIntSlice(draft, "inbound_ids")
	basePrice := draftGetFloat64(draft, "base_price")
	baseIP := draftGetInt(draft, "base_ip_limit")
	maxIP := draftGetInt(draft, "max_ip_limit")
	extraIPPrice := draftGetFloat64(draft, "price_per_extra_ip")
	flow := draftGetString(draft, "flow")
	discounts := draftGetDiscounts(draft, "discount_tiers")
	isGlobal := draftGetBool(draft, "is_global")
	syncSubs := draftGetBool(draft, "sync_subs")
	allowedUserIDs := draftGetInt64Slice(draft, "allowed_user_ids")
	isLimited := draftGetBool(draft, "is_limited")
	pricePerGB := draftGetFloat64(draft, "price_per_gb")
	minDataGB := draftGetInt64(draft, "min_data_gb")
	pricePerExtraMonth := draftGetFloat64(draft, "price_per_extra_month")

	inboundLabel := formatInboundLabel(inboundIDs)
	discountLabel := formatDiscountLabel(discounts)

	var priceBlock string
	if isLimited {
		priceBlock = fmt.Sprintf(
			"💵 Price/GB: %.0f\n"+
			"💾 Min Data: %d GB\n"+
			"⏱️ Extra Month Price: %.0f\n",
			pricePerGB, minDataGB, pricePerExtraMonth)
	} else {
		priceBlock = fmt.Sprintf("💵 Base Price: %.0f\n", basePrice)
	}

	text := fmt.Sprintf("💼 **Draft Paid Plan Config**\n\n"+
		"📝 Name: %s\n"+
		"📝 Description: %s\n"+
		"📝 Usage Notes: %s\n"+
		"📡 Inbounds: %s\n"+
		"📊 Plan Type: %s\n"+
		"%s"+
		"🌐 IP Limits: Base %d - Max %d\n"+
		"💲 Extra IP Price: %.0f\n"+
		"⚡ Flow: %s\n"+
		"🏷️ Discounts: %s\n"+
		"👥 Access: %s\n"+
		"🔄 Sync Active Subscribers: %t\n",
		nonEmpty(name, "(not set)"),
		nonEmpty(description, "(not set)"),
		nonEmpty(usageDescription, "(not set)"),
		inboundLabel,
		map[bool]string{true: "Limited", false: "Unlimited"}[isLimited],
		priceBlock,
		baseIP,
		maxIP,
		extraIPPrice,
		nonEmpty(flow, "(default/none)"),
		discountLabel,
		formatAccessLabel(isGlobal, allowedUserIDs),
		syncSubs)

	menu := &telebot.ReplyMarkup{}
	var rows []telebot.Row
	rows = append(rows, menu.Row(menu.Data("📝 Name", "admin_draft_edit", "paid:name"), menu.Data("📝 Description", "admin_draft_edit", "paid:description"), menu.Data("📝 Usage Notes", "admin_draft_edit", "paid:usage_description")))
	rows = append(rows, menu.Row(menu.Data("📡 Inbounds", "admin_draft_inbounds", "paid")))

	typeLabel := "📊 Type: Unlimited"
	if isLimited {
		typeLabel = "📊 Type: Limited"
	}
	rows = append(rows, menu.Row(menu.Data(typeLabel, "admin_draft_toggle_limited")))

	if isLimited {
		rows = append(rows, menu.Row(menu.Data("💵 Price/GB", "admin_draft_edit", "paid:price_per_gb"), menu.Data("💾 Min Data", "admin_draft_edit", "paid:min_data_gb")))
		rows = append(rows, menu.Row(menu.Data("⏱️ Extra Month Price", "admin_draft_edit", "paid:price_per_extra_month"), menu.Data("⚡ Flow", "admin_draft_edit", "paid:flow")))
	} else {
		rows = append(rows, menu.Row(menu.Data("💵 Price", "admin_draft_edit", "paid:price"), menu.Data("⚡ Flow", "admin_draft_edit", "paid:flow")))
	}

	rows = append(rows, menu.Row(menu.Data("🌐 IP Limits", "admin_draft_edit", "paid:ip_limits"), menu.Data("💲 Extra IP Price", "admin_draft_edit", "paid:extra_ip")))
	rows = append(rows, menu.Row(menu.Data("🏷️ Discounts", "admin_draft_edit", "paid:discounts"), menu.Data("👥 Access", "admin_draft_edit", "paid:access")))
	rows = append(rows, menu.Row(menu.Data("🔄 Sync Subs: "+toggleEmoji(syncSubs), "admin_draft_toggle_sync", "paid")))
	rows = append(rows, menu.Row(menu.Data("💾 Save", "admin_draft_action", "paid:save"), menu.Data("❌ Cancel", "admin_draft_action", "paid:cancel")))

	menu.Inline(rows...)
	return maybeEditOrSend(c, text, menu)
}

func HandleAdminDraftToggleLimited(c telebot.Context) error {
	user := userFromContext(c)
	if user == nil || !isConfiguredAdmin(user.TelegramID) {
		return c.Send("Permission denied.")
	}

	state := bot.FSM.GetState(user.TelegramID)
	if state == nil {
		return c.Send("No active editor flow found.")
	}
	if state.Step == "awaiting_admin_draft_input" {
		return c.Send("Please finish or cancel your current text input before using configuration buttons.")
	}

	draft := state.Data
	isLimited := draftGetBool(draft, "is_limited")
	draft["is_limited"] = !isLimited

	bot.FSM.SetState(user.TelegramID, state.Step, draft)

	return showAdminDraftPaidPlanMenu(c, draft)
}

func HandleAdminDraftEdit(c telebot.Context) error {
	user := userFromContext(c)
	if user == nil || !isConfiguredAdmin(user.TelegramID) {
		return c.Send("Permission denied.")
	}

	state := bot.FSM.GetState(user.TelegramID)
	if state == nil {
		return c.Send("No active editor flow found.")
	}

	var draft map[string]interface{}
	if state.Step == "awaiting_admin_test_plan_menu" || state.Step == "awaiting_admin_paid_plan_menu" {
		draft = state.Data
	} else if d, ok := state.Data["draft"].(map[string]interface{}); ok {
		draft = d
	} else {
		return c.Send("Invalid state.")
	}

	payload := callbackPayload(c)
	parts := strings.Split(payload, ":")
	if len(parts) != 2 {
		return c.Send("Invalid action.")
	}
	editingType := parts[0]
	editingField := parts[1]

	if editingField == "flow" {
		menu := &telebot.ReplyMarkup{}
		menu.Inline(
			menu.Row(menu.Data("⚡ xtls-rprx-vision", "admin_draft_set_flow", editingType+":xtls-rprx-vision")),
			menu.Row(menu.Data("❌ None (Clear)", "admin_draft_set_flow", editingType+":none")),
			menu.Row(menu.Data("✏️ Custom / Manual Input", "admin_draft_set_flow", editingType+":custom")),
			menu.Row(menu.Data("⬅️ Back", "admin_draft_set_flow", editingType+":back")),
		)
		currentFlow := draftGetString(draft, "flow")
		prompt := fmt.Sprintf("⚡ **Select Flow Option**:\n\n**Current**: %s", nonEmpty(currentFlow, "(default/none)"))
		return maybeEditOrSend(c, prompt, menu)
	}

	inputStateData := map[string]interface{}{
		"editing_type":  editingType,
		"editing_field": editingField,
		"draft":         draft,
	}
	bot.FSM.SetState(user.TelegramID, "awaiting_admin_draft_input", inputStateData)

	var prompt string
	switch editingField {
	case "name":
		prompt = "📝 Send the plan name (e.g., 'Monthly Standard'):"
	case "description":
		prompt = "📝 Send the plan description (e.g., 'Fast Trial'):"
	case "usage_description":
		prompt = "📝 Send the post-purchase usage description / connection notes (e.g., VLESS configs or rules):"
	case "price":
		prompt = "💵 Send the base price (e.g., '200000'):"
	case "price_per_gb":
		prompt = "💵 Send the price per GB (e.g., '10000'):"
	case "min_data_gb":
		prompt = "💾 Send the minimum dataflow in GB (e.g., '10'):"
	case "price_per_extra_month":
		prompt = "⏱️ Send the price per extra month (e.g., '20000'):"
	case "ip_limits":
		prompt = "🌐 Send the IP limits in 'base-max' format (e.g., '1-6' or '2-2'):"
	case "extra_ip":
		prompt = "💲 Send the price per extra IP (e.g., '50000'):"
	case "discounts":
		prompt = "🏷️ Send discounts in format 'months:percent,months:percent' (e.g., '3:10,6:20' or '-' for none):"
	case "access":
		prompt = "👥 Send private user Telegram IDs (comma-separated), or '-' for global access:"
	case "duration":
		prompt = "⏱️ Send duration in hours (e.g., '24' or '0.5' for 30 minutes):"
	case "max_data":
		prompt = "💾 Send max data limit in GB (e.g., '50' or '0' for unlimited):"
	case "max_per_day":
		prompt = "📊 Send max tests allowed per user per day (e.g., '2'):"
	default:
		return c.Send("Unknown editing field.")
	}

	prompt += "\n\nType /cancel to abort editing this field and return."
	return maybeEditOrSend(c, prompt)
}

func ProcessAdminDraftInput(c telebot.Context, text string) error {
	user := userFromContext(c)
	if user == nil || !isConfiguredAdmin(user.TelegramID) {
		return c.Send("You do not have permission to use this command.")
	}

	state := bot.FSM.GetState(user.TelegramID)
	if state == nil || state.Step != "awaiting_admin_draft_input" {
		return c.Send("No active editor flow found.")
	}

	editingType := state.Data["editing_type"].(string)
	editingField := state.Data["editing_field"].(string)
	draft, ok := state.Data["draft"].(map[string]interface{})
	if !ok {
		return c.Send("Failed to parse draft configuration.")
	}

	text = strings.TrimSpace(text)
	if text == "/cancel" {
		bot.FSM.SetState(user.TelegramID, "awaiting_admin_"+editingType+"_plan_menu", draft)
		if editingType == "test" {
			return showAdminDraftTestPlanMenu(c, draft)
		}
		return showAdminDraftPaidPlanMenu(c, draft)
	}

	switch editingField {
	case "name":
		if text == "" {
			return c.Send("Name cannot be empty. Send a valid name:")
		}
		draft["name"] = text
	case "description":
		draft["description"] = text
	case "usage_description":
		draft["usage_description"] = text
	case "price":
		val, err := strconv.ParseFloat(text, 64)
		if err != nil || val < 0 {
			return c.Send("Price must be zero or a positive number. Send a valid price:")
		}
		draft["base_price"] = val
	case "price_per_gb":
		val, err := strconv.ParseFloat(text, 64)
		if err != nil || val < 0 {
			return c.Send("Price per GB must be zero or a positive number. Send a valid price:")
		}
		draft["price_per_gb"] = val
	case "min_data_gb":
		val, err := strconv.ParseInt(text, 10, 64)
		if err != nil || val <= 0 {
			return c.Send("Minimum dataflow must be a positive integer in GB. Send a valid value:")
		}
		draft["min_data_gb"] = val
	case "price_per_extra_month":
		val, err := strconv.ParseFloat(text, 64)
		if err != nil || val < 0 {
			return c.Send("Price per extra month must be zero or a positive number. Send a valid price:")
		}
		draft["price_per_extra_month"] = val
	case "ip_limits":
		parts := strings.Split(text, "-")
		if len(parts) != 2 {
			return c.Send("IP limits must be in format 'base-max' (e.g. '1-6'). Try again:")
		}
		baseIP, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
		maxIP, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
		if err1 != nil || err2 != nil || baseIP <= 0 || maxIP < baseIP {
			return c.Send("Invalid IP limits. Max IP must be >= Base IP > 0. Try again:")
		}
		draft["base_ip_limit"] = baseIP
		draft["max_ip_limit"] = maxIP
	case "extra_ip":
		val, err := strconv.ParseFloat(text, 64)
		if err != nil || val < 0 {
			return c.Send("Extra IP price must be zero or a positive number. Try again:")
		}
		draft["price_per_extra_ip"] = val
	case "flow":
		if text == "-" {
			draft["flow"] = ""
		} else {
			draft["flow"] = CleanFlow(text)
		}
	case "discounts":
		if text == "-" {
			draft["discount_tiers"] = []db.DiscountTier{}
		} else {
			tiers, err := parseDiscounts(text)
			if err != nil {
				return c.Send("Invalid discounts format. Use format 'months:percent,months:percent' (e.g. '3:10,6:20') or '-' for none:")
			}
			draft["discount_tiers"] = tiers
		}
	case "access":
		if text == "-" || text == "" {
			draft["is_global"] = true
			draft["allowed_user_ids"] = []int64{}
		} else {
			userIDs, err := parseInternalUserIDs(text)
			if err != nil {
				return c.Send("Invalid user IDs. Send comma-separated internal user IDs, or '-' for global:")
			}
			draft["is_global"] = false
			draft["allowed_user_ids"] = userIDs
		}
	case "duration":
		hours, err := strconv.ParseFloat(text, 64)
		if err != nil || hours <= 0 {
			return c.Send("Duration must be a positive number of hours (e.g. '2' or '0.5'). Try again:")
		}
		draft["expire_seconds"] = int64(hours * 3600)
	case "max_data":
		gb, err := strconv.ParseFloat(text, 64)
		if err != nil || gb < 0 {
			return c.Send("Max data must be zero or a positive number in GB. Try again:")
		}
		draft["max_data_bytes"] = int64(gb * 1073741824)
	case "max_per_day":
		val, err := strconv.Atoi(text)
		if err != nil || val < 0 {
			return c.Send("Max tests per day must be zero or a positive integer. Try again:")
		}
		draft["max_per_day"] = val
	default:
		return c.Send("Unknown editing field.")
	}

	bot.FSM.SetState(user.TelegramID, "awaiting_admin_"+editingType+"_plan_menu", draft)
	if editingType == "test" {
		return showAdminDraftTestPlanMenu(c, draft)
	}
	return showAdminDraftPaidPlanMenu(c, draft)
}

func HandleAdminDraftToggleSync(c telebot.Context) error {
	user := userFromContext(c)
	if user == nil || !isConfiguredAdmin(user.TelegramID) {
		return c.Send("Permission denied.")
	}

	state := bot.FSM.GetState(user.TelegramID)
	if state == nil {
		return c.Send("No active editor flow found.")
	}
	if state.Step == "awaiting_admin_draft_input" {
		return c.Send("Please finish or cancel your current text input before using configuration buttons.")
	}

	planType := callbackPayload(c)
	draft := state.Data
	syncSubs := draftGetBool(draft, "sync_subs")
	draft["sync_subs"] = !syncSubs

	bot.FSM.SetState(user.TelegramID, state.Step, draft)

	if planType == "test" {
		return showAdminDraftTestPlanMenu(c, draft)
	}
	return showAdminDraftPaidPlanMenu(c, draft)
}

func HandleAdminDraftSetFlow(c telebot.Context) error {
	user := userFromContext(c)
	if user == nil || !isConfiguredAdmin(user.TelegramID) {
		return c.Send("Permission denied.")
	}

	state := bot.FSM.GetState(user.TelegramID)
	if state == nil {
		return c.Send("No active editor flow found.")
	}

	payload := callbackPayload(c)
	parts := strings.Split(payload, ":")
	if len(parts) != 2 {
		return c.Send("Invalid payload.")
	}

	planType := parts[0]
	choice := parts[1]

	var draft map[string]interface{}
	if state.Step == "awaiting_admin_test_plan_menu" || state.Step == "awaiting_admin_paid_plan_menu" {
		draft = state.Data
	} else if d, ok := state.Data["draft"].(map[string]interface{}); ok {
		draft = d
	} else {
		draft = state.Data
	}

	if draft == nil {
		return c.Send("Failed to load draft configuration.")
	}

	switch choice {
	case "xtls-rprx-vision":
		draft["flow"] = "xtls-rprx-vision"
		_ = c.Respond(&telebot.CallbackResponse{Text: "⚡ Flow set to xtls-rprx-vision"})
	case "none":
		draft["flow"] = ""
		_ = c.Respond(&telebot.CallbackResponse{Text: "⚡ Flow cleared"})
	case "custom":
		inputStateData := map[string]interface{}{
			"editing_type":  planType,
			"editing_field": "flow",
			"draft":         draft,
		}
		bot.FSM.SetState(user.TelegramID, "awaiting_admin_draft_input", inputStateData)
		prompt := "⚡ Send the custom flow value (e.g., 'xtls-rprx-vision-less'):\n\nType /cancel to abort editing this field and return."
		return maybeEditOrSend(c, prompt)
	case "back":
		// Do nothing
	}

	bot.FSM.SetState(user.TelegramID, "awaiting_admin_"+planType+"_plan_menu", draft)
	if planType == "test" {
		return showAdminDraftTestPlanMenu(c, draft)
	}
	return showAdminDraftPaidPlanMenu(c, draft)
}

func HandleAdminDraftAction(c telebot.Context) error {
	user := userFromContext(c)
	if user == nil || !isConfiguredAdmin(user.TelegramID) {
		return c.Send("Permission denied.")
	}

	state := bot.FSM.GetState(user.TelegramID)
	if state == nil {
		return c.Send("No active editor flow found.")
	}

	payload := callbackPayload(c)
	parts := strings.Split(payload, ":")
	if len(parts) != 2 {
		return c.Send("Invalid action.")
	}
	planType := parts[0]
	action := parts[1]

	if action == "cancel" {
		bot.FSM.ClearState(user.TelegramID)
		_ = c.Respond(&telebot.CallbackResponse{Text: "❌ Plan edit cancelled."})
		return HandleAdminPlans(c)
	}

	if state.Step == "awaiting_admin_draft_input" {
		return c.Send("Please finish or cancel your current text input before using configuration buttons.")
	}

	draft := state.Data

	if action == "save" {
		name := draftGetString(draft, "name")
		if name == "" {
			_ = c.Respond(&telebot.CallbackResponse{Text: "⚠️ Plan Name is required."})
			return c.Send("Plan Name is required. Please set it before saving.")
		}

		inboundIDs := draftGetIntSlice(draft, "inbound_ids")
		if len(inboundIDs) == 0 {
			_ = c.Respond(&telebot.CallbackResponse{Text: "⚠️ At least one inbound is required."})
			return c.Send("At least one inbound ID must be selected. Please select inbounds before saving.")
		}

		if planType == "paid" {
			isLimited := draftGetBool(draft, "is_limited")
			if isLimited {
				pricePerGB := draftGetFloat64(draft, "price_per_gb")
				if pricePerGB <= 0 {
					_ = c.Respond(&telebot.CallbackResponse{Text: "⚠️ Price per GB must be positive."})
					return c.Send("Price per GB must be positive. Please set it before saving.")
				}
				minData := draftGetInt64(draft, "min_data_gb")
				if minData <= 0 {
					_ = c.Respond(&telebot.CallbackResponse{Text: "⚠️ Minimum dataflow must be positive."})
					return c.Send("Minimum dataflow must be positive. Please set it before saving.")
				}
			} else {
				basePrice := draftGetFloat64(draft, "base_price")
				if basePrice <= 0 {
					_ = c.Respond(&telebot.CallbackResponse{Text: "⚠️ Base price must be positive."})
					return c.Send("Base price must be positive. Please set it before saving.")
				}
			}
		}

		bot.FSM.ClearState(user.TelegramID)
		_ = c.Respond(&telebot.CallbackResponse{Text: "💾 Plan saved."})
		return SaveDraftPlan(c, planType, draft)
	}

	return c.Send("Invalid action.")
}

func SaveDraftPlan(c telebot.Context, planType string, draft map[string]interface{}) error {
	name := draftGetString(draft, "name")
	inboundIDs := draftGetIntSlice(draft, "inbound_ids")
	isGlobal := draftGetBool(draft, "is_global")
	allowedUserIDs := draftGetInt64Slice(draft, "allowed_user_ids")
	id := draftGetInt64(draft, "id")

	ctx := context.Background()

	if planType == "test" {
		expireSeconds := draftGetInt64(draft, "expire_seconds")
		maxDataBytes := draftGetInt64(draft, "max_data_bytes")
		flow := draftGetString(draft, "flow")
		maxPerDay := draftGetInt(draft, "max_per_day")
		description := draftGetString(draft, "description")
		usageDescription := draftGetString(draft, "usage_description")

		plan := &db.TestPlan{
			ID:               id,
			Name:             name,
			Description:      description,
			UsageDescription: usageDescription,
			InboundIDs:       inboundIDs,
			ExpireSeconds:    expireSeconds,
			MaxDataBytes:     maxDataBytes,
			Flow:             flow,
			MaxPerDay:        maxPerDay,
			IsGlobal:         isGlobal,
			Enabled:          true,
			SyncSubs:         draftGetBool(draft, "sync_subs"),
		}

		if id > 0 {
			orig, err := db.GetTestPlanByID(ctx, id)
			if err != nil {
				return c.Send("Error retrieving original plan: " + err.Error())
			}

			if err := db.UpdateTestPlan(ctx, plan); err != nil {
				return c.Send("Failed to update test plan: " + err.Error())
			}

			if err := db.SetPlanUserAccess(ctx, db.PlanTypeTest, id, allowedUserIDs); err != nil {
				return c.Send("Plan updated, but failed to save private access: " + err.Error())
			}
			
			if orig != nil && draftGetBool(draft, "sync_subs") {
				added, removed := diffIntSlices(orig.InboundIDs, plan.InboundIDs)
				if len(added) > 0 || len(removed) > 0 {
					go syncPlanSubscriptions(id, db.PlanTypeTest, added, removed)
				}
			}

			_ = c.Send(fmt.Sprintf("✅ Updated test plan #%d: %s", id, name))
		} else {
			if err := db.CreateTestPlan(ctx, plan); err != nil {
				return c.Send("Failed to create test plan: " + err.Error())
			}

			if err := db.SetPlanUserAccess(ctx, db.PlanTypeTest, plan.ID, allowedUserIDs); err != nil {
				return c.Send("Plan created, but failed to save private access: " + err.Error())
			}

			notifyApprovedUsers("🧪 New test plan available: " + plan.Name)
			_ = c.Send(fmt.Sprintf("✅ Created test plan #%d: %s", plan.ID, name))
		}

	} else {
		description := draftGetString(draft, "description")
		usageDescription := draftGetString(draft, "usage_description")
		basePrice := draftGetFloat64(draft, "base_price")
		baseIP := draftGetInt(draft, "base_ip_limit")
		maxIP := draftGetInt(draft, "max_ip_limit")
		extraIP := draftGetFloat64(draft, "price_per_extra_ip")
		flow := draftGetString(draft, "flow")
		discounts := draftGetDiscounts(draft, "discount_tiers")
		isLimited := draftGetBool(draft, "is_limited")
		pricePerGB := draftGetFloat64(draft, "price_per_gb")
		minData := draftGetInt64(draft, "min_data_gb")
		priceExtraMonth := draftGetFloat64(draft, "price_per_extra_month")

		plan := &db.PaidPlan{
			ID:                 id,
			Name:               name,
			Description:        description,
			UsageDescription:   usageDescription,
			InboundIDs:         inboundIDs,
			BasePrice:          basePrice,
			BaseIPLimit:        baseIP,
			MaxIPLimit:         maxIP,
			PricePerExtraIP:    extraIP,
			Flow:               flow,
			DiscountTiers:      discounts,
			IsGlobal:           isGlobal,
			Enabled:            true,
			SyncSubs:           draftGetBool(draft, "sync_subs"),
			IsLimited:          isLimited,
			PricePerGB:         pricePerGB,
			MinDataGB:          minData,
			PricePerExtraMonth: priceExtraMonth,
		}

		if id > 0 {
			orig, err := db.GetPaidPlanByID(ctx, id)
			if err != nil {
				return c.Send("Error retrieving original plan: " + err.Error())
			}

			if err := db.UpdatePaidPlan(ctx, plan); err != nil {
				return c.Send("Failed to update paid plan: " + err.Error())
			}

			if err := db.SetPlanUserAccess(ctx, db.PlanTypePaid, id, allowedUserIDs); err != nil {
				return c.Send("Plan updated, but failed to save private access: " + err.Error())
			}

			if orig != nil && draftGetBool(draft, "sync_subs") {
				added, removed := diffIntSlices(orig.InboundIDs, plan.InboundIDs)
				if len(added) > 0 || len(removed) > 0 {
					go syncPlanSubscriptions(id, db.PlanTypePaid, added, removed)
				}
			}

			_ = c.Send(fmt.Sprintf("✅ Updated paid plan #%d: %s", id, name))
		} else {
			if err := db.CreatePaidPlan(ctx, plan); err != nil {
				return c.Send("Failed to create paid plan: " + err.Error())
			}

			if err := db.SetPlanUserAccess(ctx, db.PlanTypePaid, plan.ID, allowedUserIDs); err != nil {
				return c.Send("Plan created, but failed to save private access: " + err.Error())
			}

			notifyApprovedUsers("💼 New paid plan available: " + plan.Name)
			_ = c.Send(fmt.Sprintf("✅ Created paid plan #%d: %s", plan.ID, name))
		}
	}

	return HandleAdminPlans(c)
}

func syncPlanSubscriptions(planID int64, planType string, addedIDs, removedIDs []int) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[PANIC RECOVERY] syncPlanSubscriptions panicked: %v", r)
		}
	}()

	ctx := context.Background()
	subs, err := db.GetActiveSubscriptionsByPlan(ctx, planType, planID)
	if err != nil {
		log.Printf("Sync error: failed to fetch active subscriptions for plan %s:%d: %v", planType, planID, err)
		return
	}
	if len(subs) == 0 {
		return
	}

	emails := make([]string, 0, len(subs))
	for _, sub := range subs {
		emails = append(emails, sub.ClientEmail)
	}

	if bot.XUIClient == nil {
		log.Printf("Sync error: XUIClient is nil")
		return
	}

	if bot.XUIClient.Cache != nil {
		bot.XUIClient.Cache.RefreshSync()
	}
	cachedInbounds := bot.XUIClient.GetCachedInbounds()
	known := make(map[int]bool)
	for _, inbound := range cachedInbounds {
		known[inbound.ID] = true
	}

	validAdded := make([]int, 0, len(addedIDs))
	for _, id := range addedIDs {
		if known[id] {
			validAdded = append(validAdded, id)
		}
	}

	if len(validAdded) > 0 {
		req := xui.BulkAttachRequest{
			Emails:     emails,
			InboundIDs: validAdded,
		}
		if err := bot.XUIClient.BulkAttach(req); err != nil {
			log.Printf("Sync error: BulkAttach failed: %v", err)
		} else {
			log.Printf("Sync success: BulkAttach of %d client(s) to inbound(s) %v completed", len(emails), validAdded)
		}
	}

	validRemoved := make([]int, 0, len(removedIDs))
	for _, id := range removedIDs {
		if known[id] {
			validRemoved = append(validRemoved, id)
		}
	}

	if len(validRemoved) > 0 {
		req := xui.BulkAttachRequest{
			Emails:     emails,
			InboundIDs: validRemoved,
		}
		if err := bot.XUIClient.BulkDetach(req); err != nil {
			log.Printf("Sync error: BulkDetach failed: %v", err)
		} else {
			log.Printf("Sync success: BulkDetach of %d client(s) from inbound(s) %v completed", len(emails), validRemoved)
		}
	}
}

func HandleAdminDraftInboundsMenu(c telebot.Context) error {
	user := userFromContext(c)
	if user == nil || !isConfiguredAdmin(user.TelegramID) {
		return c.Send("Permission denied.")
	}

	state := bot.FSM.GetState(user.TelegramID)
	if state == nil {
		return c.Send("No active editor flow found.")
	}
	if state.Step == "awaiting_admin_draft_input" {
		return c.Send("Please finish or cancel your current text input before using configuration buttons.")
	}

	planType := callbackPayload(c)
	draft := state.Data

	return showAdminDraftInboundsMenu(c, planType, draft)
}

func showAdminDraftInboundsMenu(c telebot.Context, planType string, draft map[string]interface{}) error {
	if bot.XUIClient == nil {
		return c.Send("x-ui client is not initialized.")
	}

	cached := bot.XUIClient.GetCachedInbounds()
	selectedIDs := draftGetIntSlice(draft, "inbound_ids")

	selectedMap := make(map[int]bool)
	for _, id := range selectedIDs {
		selectedMap[id] = true
	}

	var text strings.Builder
	text.WriteString("📡 **Select Inbounds for Plan**\n\n")
	text.WriteString("Toggle the inbounds you want to attach to this plan. You can use 'Select All' to mark all, and then uncheck any individual inbounds.\n\n")
	text.WriteString("Current Selection:\n")
	
	inboundNames := cachedInboundNames()
	if len(selectedIDs) == 0 {
		text.WriteString("  _(none selected)_\n")
	} else {
		for _, id := range selectedIDs {
			if name, ok := inboundNames[id]; ok && name != "" {
				text.WriteString(fmt.Sprintf("  ✅ ID %d: %s\n", id, name))
			} else {
				text.WriteString(fmt.Sprintf("  ✅ ID %d\n", id))
			}
		}
	}

	menu := &telebot.ReplyMarkup{}
	var rows []telebot.Row

	for _, inbound := range cached {
		isSelected := selectedMap[inbound.ID]
		mark := "❌"
		if isSelected {
			mark = "✅"
		}
		btnText := fmt.Sprintf("%s ID %d: %s (%s:%d)", mark, inbound.ID, inbound.Remark, inbound.Protocol, inbound.Port)
		rows = append(rows, menu.Row(menu.Data(btnText, "admin_draft_toggle_inbound", fmt.Sprintf("%s:%d", planType, inbound.ID))))
	}

	allSelected := true
	for _, inbound := range cached {
		if !selectedMap[inbound.ID] {
			allSelected = false
			break
		}
	}

	var actionRow telebot.Row
	if allSelected && len(cached) > 0 {
		actionRow = menu.Row(menu.Data("❌ Deselect All", "admin_draft_toggle_all", planType+":deselect"))
	} else {
		actionRow = menu.Row(menu.Data("✅ Select All", "admin_draft_toggle_all", planType+":select"))
	}
	rows = append(rows, actionRow)
	rows = append(rows, menu.Row(menu.Data("💾 Done", "admin_draft_inbounds_done", planType)))

	menu.Inline(rows...)
	return maybeEditOrSend(c, text.String(), menu)
}

func HandleAdminDraftToggleInbound(c telebot.Context) error {
	user := userFromContext(c)
	if user == nil || !isConfiguredAdmin(user.TelegramID) {
		return c.Send("Permission denied.")
	}

	state := bot.FSM.GetState(user.TelegramID)
	if state == nil {
		return c.Send("No active editor flow found.")
	}
	if state.Step == "awaiting_admin_draft_input" {
		return c.Send("Please finish or cancel your current text input before using configuration buttons.")
	}

	payload := callbackPayload(c)
	parts := strings.Split(payload, ":")
	if len(parts) != 2 {
		return c.Send("Invalid parameter.")
	}
	planType := parts[0]
	inboundID, err := strconv.Atoi(parts[1])
	if err != nil {
		return c.Send("Invalid inbound ID.")
	}

	draft := state.Data
	inboundIDs := draftGetIntSlice(draft, "inbound_ids")

	found := false
	newIDs := make([]int, 0, len(inboundIDs))
	for _, id := range inboundIDs {
		if id == inboundID {
			found = true
		} else {
			newIDs = append(newIDs, id)
		}
	}
	if !found {
		newIDs = append(newIDs, inboundID)
	}
	draft["inbound_ids"] = newIDs

	bot.FSM.SetState(user.TelegramID, state.Step, draft)
	_ = c.Respond(&telebot.CallbackResponse{Text: "Toggled inbound."})
	return showAdminDraftInboundsMenu(c, planType, draft)
}

func HandleAdminDraftToggleAll(c telebot.Context) error {
	user := userFromContext(c)
	if user == nil || !isConfiguredAdmin(user.TelegramID) {
		return c.Send("Permission denied.")
	}

	state := bot.FSM.GetState(user.TelegramID)
	if state == nil {
		return c.Send("No active editor flow found.")
	}
	if state.Step == "awaiting_admin_draft_input" {
		return c.Send("Please finish or cancel your current text input before using configuration buttons.")
	}

	payload := callbackPayload(c)
	parts := strings.Split(payload, ":")
	if len(parts) != 2 {
		return c.Send("Invalid parameter.")
	}
	planType := parts[0]
	action := parts[1]

	draft := state.Data

	if action == "select" {
		if bot.XUIClient != nil {
			cached := bot.XUIClient.GetCachedInbounds()
			newIDs := make([]int, 0, len(cached))
			for _, inbound := range cached {
				newIDs = append(newIDs, inbound.ID)
			}
			draft["inbound_ids"] = newIDs
		}
		_ = c.Respond(&telebot.CallbackResponse{Text: "Selected all inbounds."})
	} else {
		draft["inbound_ids"] = []int{}
		_ = c.Respond(&telebot.CallbackResponse{Text: "Deselected all inbounds."})
	}

	bot.FSM.SetState(user.TelegramID, state.Step, draft)
	return showAdminDraftInboundsMenu(c, planType, draft)
}

func HandleAdminDraftInboundsDone(c telebot.Context) error {
	user := userFromContext(c)
	if user == nil || !isConfiguredAdmin(user.TelegramID) {
		return c.Send("Permission denied.")
	}

	state := bot.FSM.GetState(user.TelegramID)
	if state == nil {
		return c.Send("No active editor flow found.")
	}
	if state.Step == "awaiting_admin_draft_input" {
		return c.Send("Please finish or cancel your current text input before using configuration buttons.")
	}

	planType := callbackPayload(c)
	draft := state.Data

	_ = c.Respond(&telebot.CallbackResponse{Text: "Inbounds saved."})

	if planType == "test" {
		return showAdminDraftTestPlanMenu(c, draft)
	}
	return showAdminDraftPaidPlanMenu(c, draft)
}

func HandleAdminViewPlan(c telebot.Context) error {
	planType, planID, ok := parsePlanRef(callbackPayload(c))
	if !ok {
		return c.Send("Invalid plan.")
	}
	return showAdminViewPlan(c, planType, planID)
}

func showAdminViewPlan(c telebot.Context, planType string, planID int64) error {
	inboundNames := cachedInboundNames()
	inboundLabel := func(ids []int) string {
		parts := make([]string, 0, len(ids))
		for _, id := range ids {
			if name, ok := inboundNames[id]; ok && name != "" {
				parts = append(parts, fmt.Sprintf("%d (%s)", id, name))
			} else {
				parts = append(parts, fmt.Sprintf("%d", id))
			}
		}
		return strings.Join(parts, ", ")
	}

	var text string
	var enabled bool
	if planType == db.PlanTypeTest {
		plan, err := db.GetTestPlanByID(context.Background(), planID)
		if err != nil || plan == nil {
			return c.Send("Test plan not found.")
		}
		enabled = plan.Enabled
		access, _ := db.GetPlanUserAccess(context.Background(), planType, planID)
		text = fmt.Sprintf("🧪 **Test plan #%d**\n"+
			"Name: %s\nDescription: %s\nUsage Notes: %s\nEnabled: %t\nGlobal: %t\n"+
			"Inbounds: %s\nDuration: %s\nMax data: %.2f GB\n"+
			"Flow: %s\nMax/day: %d\nPrivate users: %v",
			plan.ID, plan.Name, plan.Description, plan.UsageDescription, plan.Enabled, plan.IsGlobal,
			inboundLabel(plan.InboundIDs), humanDuration(plan.ExpireSeconds),
			float64(plan.MaxDataBytes)/1073741824, plan.Flow, plan.MaxPerDay, access)
	} else {
		plan, err := db.GetPaidPlanByID(context.Background(), planID)
		if err != nil || plan == nil {
			return c.Send("Paid plan not found.")
		}
		enabled = plan.Enabled
		access, _ := db.GetPlanUserAccess(context.Background(), planType, planID)

		var priceBlock string
		if plan.IsLimited {
			priceBlock = fmt.Sprintf("Type: Limited\nPrice/GB: %.0f\nMin Data: %d GB\nExtra Month Price: %.0f", plan.PricePerGB, plan.MinDataGB, plan.PricePerExtraMonth)
		} else {
			priceBlock = fmt.Sprintf("Type: Unlimited\nBase price: %.0f", plan.BasePrice)
		}

		text = fmt.Sprintf("💼 **Paid plan #%d**\n"+
			"Name: %s\nDescription: %s\nUsage Notes: %s\nEnabled: %t\nGlobal: %t\n"+
			"Inbounds: %s\n%s\nIP: %d-%d\n"+
			"Extra IP: %.0f\nFlow: %s\nDiscounts: %+v\nPrivate users: %v",
			plan.ID, plan.Name, plan.Description, plan.UsageDescription, plan.Enabled, plan.IsGlobal,
			inboundLabel(plan.InboundIDs), priceBlock, plan.BaseIPLimit, plan.MaxIPLimit,
			plan.PricePerExtraIP, plan.Flow, plan.DiscountTiers, access)
	}

	toggleText := "🔴 Disable"
	if !enabled {
		toggleText = "✅ Enable"
	}
	ref := fmt.Sprintf("%s:%d", planType, planID)
	menu := &telebot.ReplyMarkup{}
	menu.Inline(
		menu.Row(menu.Data("📝 Edit Plan", "admin_plan_edit", ref)),
		menu.Row(menu.Data(toggleText, "admin_plan_toggle", ref), menu.Data("👥 Set private users", "admin_plan_access", ref)),
		menu.Row(menu.Data("🗑 Delete", "admin_plan_delete", ref)),
		menu.Row(menu.Data("« Back", "admin_plans")),
	)
	return maybeEditOrSend(c, text, menu)
}

func HandleAdminPlanToggle(c telebot.Context) error {
	planType, planID, ok := parsePlanRef(callbackPayload(c))
	if !ok {
		return c.Send("Invalid plan.")
	}
	enabled := false
	if planType == db.PlanTypeTest {
		plan, _ := db.GetTestPlanByID(context.Background(), planID)
		if plan == nil {
			return c.Send("Plan not found.")
		}
		enabled = !plan.Enabled
	} else {
		plan, _ := db.GetPaidPlanByID(context.Background(), planID)
		if plan == nil {
			return c.Send("Plan not found.")
		}
		enabled = !plan.Enabled
	}
	if err := db.SetPlanEnabled(context.Background(), planType, planID, enabled); err != nil {
		return c.Send("Failed to update plan.")
	}
	status := "disabled"
	if enabled {
		status = "enabled"
	}
	_ = c.Respond(&telebot.CallbackResponse{Text: fmt.Sprintf("✅ Plan %s.", status)})
	return showAdminViewPlan(c, planType, planID)
}

func HandleAdminPlanDelete(c telebot.Context) error {
	planType, planID, ok := parsePlanRef(callbackPayload(c))
	if !ok {
		return c.Send("Invalid plan.")
	}
	if err := db.DeletePlan(context.Background(), planType, planID); err != nil {
		return c.Send("Failed to delete plan: " + err.Error())
	}
	_ = c.Respond(&telebot.CallbackResponse{Text: "🗑 Plan deleted."})
	return HandleAdminPlans(c)
}

func HandleAdminPlanAccessPrompt(c telebot.Context) error {
	user := userFromContext(c)
	planType, planID, ok := parsePlanRef(callbackPayload(c))
	if !ok || user == nil {
		return c.Send("Invalid plan.")
	}
	bot.FSM.SetState(user.TelegramID, "awaiting_admin_plan_access", map[string]interface{}{
		"plan_type": planType,
		"plan_id":   fmt.Sprintf("%d", planID),
	})
	return maybeEditOrSend(c, "Send internal bot user IDs (comma-separated) to grant private access.\nSend empty or '-' to make this plan global.")
}

func ProcessAdminPlanAccess(c telebot.Context, text string) error {
	user := userFromContext(c)
	if user == nil || !isConfiguredAdmin(user.TelegramID) {
		return c.Send("You do not have permission to use this command.")
	}
	state := bot.FSM.GetState(user.TelegramID)
	if state == nil {
		return c.Send("No plan access flow is active.")
	}
	planType := fmt.Sprintf("%v", state.Data["plan_type"])
	planID, _ := parseInt64(fmt.Sprintf("%v", state.Data["plan_id"]))
	userIDs, err := parseInternalUserIDs(text)
	if err != nil {
		return c.Send("Invalid user IDs. Use internal bot user IDs, comma-separated.")
	}
	if err := db.SetPlanUserAccess(context.Background(), planType, planID, userIDs); err != nil {
		return c.Send("Failed to save private access.")
	}
	if err := db.SetPlanGlobal(context.Background(), planType, planID, len(userIDs) == 0); err != nil {
		return c.Send("Private access saved, but failed to update plan visibility.")
	}
	bot.FSM.ClearState(user.TelegramID)
	if len(userIDs) == 0 {
		_ = c.Send("✅ Plan is now global (visible to all approved users).")
	} else {
		_ = c.Send(fmt.Sprintf("✅ Private plan access updated for %d user(s).", len(userIDs)))
	}
	return showAdminViewPlan(c, planType, planID)
}

func notifyApprovedUsers(text string) {
	go func() {
		if bot.Bot == nil {
			return
		}
		users, err := db.ListApprovedUsers(context.Background())
		if err != nil {
			return
		}
		for _, user := range users {
			_, _ = bot.Bot.Send(&telebot.User{ID: user.TelegramID}, text)
			time.Sleep(35 * time.Millisecond)
		}
	}()
}

// ─── Draft Getters ────────────────────────────────────────────────────────────

func draftGetString(m map[string]interface{}, key string) string {
	if val, ok := m[key]; ok {
		if s, ok := val.(string); ok {
			return s
		}
	}
	return ""
}

func draftGetInt(m map[string]interface{}, key string) int {
	if val, ok := m[key]; ok {
		switch v := val.(type) {
		case int:
			return v
		case int64:
			return int(v)
		case float64:
			return int(v)
		}
	}
	return 0
}

func draftGetInt64(m map[string]interface{}, key string) int64 {
	if val, ok := m[key]; ok {
		switch v := val.(type) {
		case int64:
			return v
		case int:
			return int64(v)
		case float64:
			return int64(v)
		}
	}
	return 0
}

func draftGetFloat64(m map[string]interface{}, key string) float64 {
	if val, ok := m[key]; ok {
		switch v := val.(type) {
		case float64:
			return v
		case int:
			return float64(v)
		case int64:
			return float64(v)
		}
	}
	return 0.0
}

func draftGetBool(m map[string]interface{}, key string) bool {
	if val, ok := m[key]; ok {
		if b, ok := val.(bool); ok {
			return b
		}
	}
	return false
}

func draftGetIntSlice(m map[string]interface{}, key string) []int {
	if val, ok := m[key]; ok {
		if slice, ok := val.([]int); ok {
			return slice
		}
		if slice, ok := val.([]interface{}); ok {
			out := make([]int, 0, len(slice))
			for _, item := range slice {
				switch v := item.(type) {
				case float64:
					out = append(out, int(v))
				case int:
					out = append(out, v)
				case int64:
					out = append(out, int(v))
				}
			}
			return out
		}
	}
	return []int{}
}

func draftGetInt64Slice(m map[string]interface{}, key string) []int64 {
	if val, ok := m[key]; ok {
		if slice, ok := val.([]int64); ok {
			return slice
		}
		if slice, ok := val.([]interface{}); ok {
			out := make([]int64, 0, len(slice))
			for _, item := range slice {
				switch v := item.(type) {
				case float64:
					out = append(out, int64(v))
				case int64:
					out = append(out, v)
				case int:
					out = append(out, int64(v))
				}
			}
			return out
		}
	}
	return []int64{}
}

func draftGetDiscounts(m map[string]interface{}, key string) []db.DiscountTier {
	if val, ok := m[key]; ok {
		if tiers, ok := val.([]db.DiscountTier); ok {
			return tiers
		}
		if slice, ok := val.([]interface{}); ok {
			out := make([]db.DiscountTier, 0, len(slice))
			for _, item := range slice {
				if d, ok := item.(db.DiscountTier); ok {
					out = append(out, d)
				} else if dMap, ok := item.(map[string]interface{}); ok {
					months := 0
					percent := 0.0
					if mVal, ok := dMap["months"]; ok {
						if mInt, ok := mVal.(float64); ok {
							months = int(mInt)
						} else if mInt, ok := mVal.(int); ok {
							months = mInt
						}
					}
					if pVal, ok := dMap["percent"]; ok {
						if pFloat, ok := pVal.(float64); ok {
							percent = pFloat
						}
					}
					if months > 0 {
						out = append(out, db.DiscountTier{Months: months, Percent: percent})
					}
				}
			}
			return out
		}
	}
	return []db.DiscountTier{}
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

func splitSpec(text string) []string {
	raw := strings.Split(text, "|")
	parts := make([]string, 0, len(raw))
	for _, part := range raw {
		parts = append(parts, strings.TrimSpace(part))
	}
	return parts
}

func parseInboundIDs(text string) ([]int, error) {
	chunks := strings.Split(text, ",")
	ids := make([]int, 0, len(chunks))
	for _, chunk := range chunks {
		id, err := strconv.Atoi(strings.TrimSpace(chunk))
		if err != nil || id <= 0 {
			return nil, fmt.Errorf("invalid inbound id")
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func parseInternalUserIDs(text string) ([]int64, error) {
	text = strings.TrimSpace(text)
	if text == "" || text == "-" {
		return nil, nil
	}
	chunks := strings.Split(text, ",")
	ids := make([]int64, 0, len(chunks))
	for _, chunk := range chunks {
		id, err := parseInt64(strings.TrimSpace(chunk))
		if err != nil || id <= 0 {
			return nil, fmt.Errorf("invalid user id")
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func parsePlanRef(payload string) (string, int64, bool) {
	parts := strings.Split(payload, ":")
	if len(parts) != 2 {
		return "", 0, false
	}
	planType := strings.TrimSpace(parts[0])
	if planType != db.PlanTypeTest && planType != db.PlanTypePaid {
		return "", 0, false
	}
	planID, err := parseInt64(parts[1])
	if err != nil || planID <= 0 {
		return "", 0, false
	}
	return planType, planID, true
}

func parseDiscounts(text string) ([]db.DiscountTier, error) {
	text = strings.TrimSpace(text)
	if text == "" || text == "-" {
		return nil, nil
	}
	chunks := strings.Split(text, ",")
	tiers := make([]db.DiscountTier, 0, len(chunks))
	for _, chunk := range chunks {
		pair := strings.Split(strings.TrimSpace(chunk), ":")
		if len(pair) != 2 {
			return nil, fmt.Errorf("invalid discount")
		}
		months, err := strconv.Atoi(strings.TrimSpace(pair[0]))
		if err != nil || months <= 0 {
			return nil, err
		}
		percent, err := strconv.ParseFloat(strings.TrimSpace(pair[1]), 64)
		if err != nil || percent < 0 || percent > 100 {
			return nil, err
		}
		tiers = append(tiers, db.DiscountTier{Months: months, Percent: percent})
	}
	return tiers, nil
}

func cachedInboundNames() map[int]string {
	names := make(map[int]string)
	if bot.XUIClient != nil {
		for _, inbound := range bot.XUIClient.GetCachedInbounds() {
			names[inbound.ID] = inbound.Remark
		}
	}
	return names
}

func humanDuration(seconds int64) string {
	if seconds <= 0 {
		return "unlimited"
	}
	hours := seconds / 3600
	if hours < 24 {
		return fmt.Sprintf("%d hour(s)", hours)
	}
	days := hours / 24
	if days < 7 {
		return fmt.Sprintf("%d day(s)", days)
	}
	weeks := days / 7
	if weeks < 5 {
		return fmt.Sprintf("%d week(s)", weeks)
	}
	months := days / 30
	return fmt.Sprintf("%d month(s)", months)
}

func nonEmpty(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

func formatInboundLabel(ids []int) string {
	if len(ids) == 0 {
		return "(none selected)"
	}
	inboundNames := cachedInboundNames()
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		if name, ok := inboundNames[id]; ok && name != "" {
			parts = append(parts, fmt.Sprintf("%d (%s)", id, name))
		} else {
			parts = append(parts, fmt.Sprintf("%d", id))
		}
	}
	return strings.Join(parts, ", ")
}

func formatDiscountLabel(tiers []db.DiscountTier) string {
	if len(tiers) == 0 {
		return "none"
	}
	parts := make([]string, 0, len(tiers))
	for _, t := range tiers {
		parts = append(parts, fmt.Sprintf("%dm:%g%%", t.Months, t.Percent))
	}
	return strings.Join(parts, ", ")
}

func formatAccessLabel(isGlobal bool, allowedUserIDs []int64) string {
	if isGlobal {
		return "Global (All Users)"
	}
	if len(allowedUserIDs) == 0 {
		return "Private (No Users)"
	}
	parts := make([]string, 0, len(allowedUserIDs))
	for _, id := range allowedUserIDs {
		parts = append(parts, fmt.Sprintf("%d", id))
	}
	return fmt.Sprintf("Private (Users: %s)", strings.Join(parts, ", "))
}

func toggleEmoji(val bool) string {
	if val {
		return "✅"
	}
	return "❌"
}

