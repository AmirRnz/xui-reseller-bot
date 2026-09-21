package persian

import (
	"fmt"
	"strings"
)

// Standard Navigation & Action Button Texts (Persian)
const (
	BtnBack         = "« بازگشت"
	BtnCancel       = "❌ انصراف"
	BtnConfirm      = "✅ تایید"
	BtnWallet       = "💰 کیف پول"
	BtnMyServices   = "📋 سرویس‌های من"
	BtnBuySub       = "💼 خرید سرویس"
	BtnFreeTest     = "🧪 تست رایگان"
	BtnSupport      = "🆘 پشتیبانی"
	BtnAdmin        = "⚙️ پنل مدیریت"
	BtnRefresh      = "🔄 بروزرسانی"
	BtnExtend       = "🔄 تمدید اشتراک"
	BtnUpgradeIP    = "📶 افزایش سقف آی‌پی همزمان"
	BtnGetLink      = "🔗 دریافت لینک اتصال"
	BtnToggleStatus = "⏯ قطع / وصل سرویس"
	BtnRename       = "✏️ تغییر نام سرویس"
	BtnDelete       = "🗑 حذف سرویس"
)

// Admin Button Texts (Persian)
const (
	BtnAdminCreateClient = "➕ ایجاد کاربر جدید"
	BtnAdminUsers        = "👥 مدیریت کاربران"
	BtnAdminPlans        = "📋 مدیریت طرح‌ها"
	BtnAdminSettings     = "⚙️ تنظیمات سیستم"
	BtnAdminStats        = "📊 گزارش و آمار"
	BtnAdminTopups       = "📥 افزایش موجودی‌های در انتظار"
	BtnAdminClaims       = "🔍 ثبت‌های در انتظار تایید"
	BtnAdminReconcile    = "🔄 وضعیت تطبیق و عملیات"
)

// FormatMoney formats an integer amount with thousands separators and the Toman unit.
func FormatMoney(amount int64) string {
	sign := ""
	if amount < 0 {
		sign = "-"
		amount = -amount
	}
	str := fmt.Sprintf("%d", amount)
	n := len(str)
	if n <= 3 {
		return fmt.Sprintf("%s%s تومان", sign, str)
	}

	var sb strings.Builder
	remainder := n % 3
	if remainder > 0 {
		sb.WriteString(str[:remainder])
		if n > remainder {
			sb.WriteString(",")
		}
	}
	for i := remainder; i < n; i += 3 {
		sb.WriteString(str[i : i+3])
		if i+3 < n {
			sb.WriteString(",")
		}
	}
	return fmt.Sprintf("%s%s تومان", sign, sb.String())
}

// FormatTraffic converts bytes into a Persian GB string.
func FormatTraffic(bytes int64) string {
	if bytes <= 0 {
		return "نامحدود"
	}
	gb := float64(bytes) / 1073741824.0
	if gb == float64(int64(gb)) {
		return fmt.Sprintf("%d گیگابایت", int64(gb))
	}
	return fmt.Sprintf("%.1f گیگابایت", gb)
}

// FormatIPLimit formats concurrent IP connection limits with explicit Persian phrasing.
func FormatIPLimit(limit int) string {
	if limit <= 0 {
		return "نامحدود"
	}
	return fmt.Sprintf("حداکثر %d آی‌پی همزمان", limit)
}

// FormatDuration formats duration in days.
func FormatDuration(days int) string {
	if days <= 0 {
		return "نامشخص"
	}
	if days%30 == 0 {
		months := days / 30
		return fmt.Sprintf("%d ماه (%d روز)", months, days)
	}
	return fmt.Sprintf("%d روز", days)
}

// SafeErrorMessage maps an error to a customer-safe Persian explanation.
// Internal error details (%v, err.Error()) are NEVER exposed.
func SafeErrorMessage(err error, trackingID string) string {
	var base string
	if err == nil {
		base = "عملیات با موفقیت انجام شد."
	} else {
		base = "متأسفانه در انجام عملیات خطایی رخ داده است. لطفاً لحظاتی دیگر مجدداً تلاش نمایید."
	}

	if trackingID != "" {
		return fmt.Sprintf("%s\n\n📌 **شناسه پیگیری:** `%s`", base, trackingID)
	}
	return base
}
