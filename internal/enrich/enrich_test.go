package enrich

import (
	"strings"
	"testing"
	"time"
)

type corpusCase struct {
	name        string
	in          Input
	wantCat     string
	wantSub     string
	wantKind    string
	wantValue   string
	wantRuleHas string // substring of the rule, "" = don't check
}

func TestCorpus(t *testing.T) {
	cases := []corpusCase{
		// --- Codes: happy paths ---
		{name: "nbp otp in subject", in: Input{FromEmail: "alerts@nbp.com.pk", Subject: "Your NBP OTP is 482619", BodyHead: "Dear customer, your one-time password is 482619. It expires in 10 minutes."}, wantCat: CategoryAutomated, wantSub: "Transactions", wantKind: ExtractCode, wantValue: "482619"},
		{name: "askari otp in body", in: Input{FromEmail: "noreply@askaribank.com.pk", Subject: "Askari Bank Verification", BodyHead: "Your verification code is 739201. Do not share it with anyone."}, wantCat: CategoryAutomated, wantSub: "Transactions", wantKind: ExtractCode, wantValue: "739201"},
		{name: "google code in subject", in: Input{FromEmail: "no-reply@google.com", Subject: "G-482619 is your Google verification code", BodyHead: "G-482619 is your Google verification code."}, wantCat: CategoryAutomated, wantSub: "Transactions", wantKind: ExtractCode, wantValue: "482619"},
		{name: "roman urdu otp", in: Input{FromEmail: "alerts@jazzcash.com.pk", Subject: "JazzCash verification", BodyHead: "aap ka verification code 917364 hai, kisi ko na batayen."}, wantCat: CategoryAutomated, wantSub: "Transactions", wantKind: ExtractCode, wantValue: "917364"},
		{name: "pin code", in: Input{FromEmail: "donotreply@hbl.com", Subject: "Your HBL PIN", BodyHead: "Your transaction PIN is 4455. Keep it secret."}, wantCat: CategoryAutomated, wantSub: "Transactions", wantKind: ExtractCode, wantValue: "4455"},
		{name: "8 digit code", in: Input{FromEmail: "no-reply@microsoft.com", Subject: "Microsoft account security code", BodyHead: "Use this security code to sign in: 84739201"}, wantCat: CategoryAutomated, wantSub: "Transactions", wantKind: ExtractCode, wantValue: "84739201"},
		{name: "code in snippet only", in: Input{FromEmail: "alerts@ubl.com.pk", Subject: "UBL Alerts", Snippet: "Your OTP is 284716 for login", BodyHead: ""}, wantCat: CategoryAutomated, wantSub: "Transactions", wantKind: ExtractCode, wantValue: "284716"},
		{name: "known sender bare code in subject", in: Input{FromEmail: "alerts@nbp.com.pk", Subject: "482619", BodyHead: "Your login attempt."}, wantCat: CategoryAutomated, wantSub: "Transactions", wantKind: ExtractCode, wantValue: "482619"},
		{name: "code beats reference", in: Input{FromEmail: "noreply@pia.com.pk", Subject: "Your booking reference NB472 and verification code 553821", BodyHead: "Booking reference NB472. Your verification code is 553821."}, wantCat: CategoryAutomated, wantSub: "Travel", wantKind: ExtractCode, wantValue: "553821"},

		// --- References ---
		{name: "pnr reference", in: Input{FromEmail: "noreply@pia.com.pk", Subject: "Your flight booking", BodyHead: "Your booking reference (PNR) is NB472. Have a pleasant flight."}, wantCat: CategoryAutomated, wantSub: "Travel", wantKind: ExtractReference, wantValue: "NB472"},
		{name: "order reference", in: Input{FromEmail: "orders@daraz.pk", Subject: "Your order is confirmed", BodyHead: "Thank you! Your order number is ORD884210. It will ship soon."}, wantCat: CategoryAutomated, wantSub: "Orders", wantKind: ExtractReference, wantValue: "ORD884210"},
		{name: "six letter pnr", in: Input{FromEmail: "noreply@emirates.com", Subject: "Booking confirmation", BodyHead: "Your PNR is KXTRPQ. Manage your booking online."}, wantCat: CategoryAutomated, wantSub: "Travel", wantKind: ExtractReference, wantValue: "KXTRPQ"},
		{name: "hotel confirmation number", in: Input{FromEmail: "noreply@booking.com", Subject: "Booking confirmation", BodyHead: "Your confirmation number is BK947120. Check-in from 3pm."}, wantCat: CategoryAutomated, wantSub: "Travel", wantKind: ExtractReference, wantValue: "BK947120"},

		// --- Traps: must extract nothing ---
		{name: "invoice total is not a code", in: Input{FromEmail: "billing@netflix.com", Subject: "Your invoice is ready", ListUnsubscribe: "<https://netflix.com/unsubscribe>", BodyHead: "Your total is $15.49 for this month. Your invoice number is 48261933."}, wantCat: CategoryAutomated, wantSub: "", wantKind: "", wantValue: ""},
		{name: "amount with currency", in: Input{FromEmail: "alerts@hbl.com", Subject: "Transaction alert", BodyHead: "Your account was debited Rs 4,826.19 at ATM. Your verification code is 112233."}, wantCat: CategoryAutomated, wantSub: "Transactions", wantKind: ExtractCode, wantValue: "112233"},
		{name: "date that looks like a code", in: Input{FromEmail: "noreply@pia.com.pk", Subject: "Flight on 482619", BodyHead: "Your flight departs 12/06/2026. Please arrive early."}, wantCat: CategoryAutomated, wantSub: "Travel", wantKind: "", wantValue: ""},
		{name: "year in subject", in: Input{FromEmail: "newsletter@nytimes.com", Subject: "The best of 2026", ListUnsubscribe: "<https://nytimes.com/unsubscribe>", BodyHead: "A look back at 2026 in review."}, wantCat: CategoryAutomated, wantSub: "", wantKind: "", wantValue: ""},
		{name: "phone fragment", in: Input{FromEmail: "support@careem.com", Subject: "Your ride receipt", BodyHead: "Call your captain on 0300 1234567. Your verification code is 998877."}, wantCat: CategoryAutomated, wantSub: "Orders", wantKind: ExtractCode, wantValue: "998877"},
		{name: "phone fragment alone, no code", in: Input{FromEmail: "support@careem.com", Subject: "Contact your driver", BodyHead: "Call your captain on 0300 1234567 if you need help."}, wantCat: CategoryAutomated, wantSub: "Orders", wantKind: "", wantValue: ""},
		{name: "ambiguous two codes", in: Input{FromEmail: "alerts@nbp.com.pk", Subject: "Two codes", BodyHead: "Your OTP is 482619. Your other code is 739201."}, wantCat: CategoryAutomated, wantSub: "Transactions", wantKind: "", wantValue: ""},
		{name: "code without keyword from unknown sender", in: Input{FromEmail: "hello@unknown-bank.example", Subject: "Hello", BodyHead: "Your number is 482619, thank you."}, wantCat: CategoryPersonal, wantSub: "", wantKind: "", wantValue: ""},
		{name: "tracking number not extracted", in: Input{FromEmail: "noreply@tcs.com.pk", Subject: "Your parcel is on its way", BodyHead: "Your tracking number is 1Z999AA10123456784. Track it online."}, wantCat: CategoryAutomated, wantSub: "Shipping", wantKind: "", wantValue: ""},
		{name: "decimal amount", in: Input{FromEmail: "alerts@meezanbank.com", Subject: "Debit alert", BodyHead: "PKR 12,500.00 was debited from your account ending 4826."}, wantCat: CategoryAutomated, wantSub: "Transactions", wantKind: "", wantValue: ""},

		// --- Categories: domains ---
		{name: "bank domain transactions", in: Input{FromEmail: "alerts@hbl.com", Subject: "Monthly statement is ready", BodyHead: "Your statement for November is ready to view."}, wantCat: CategoryAutomated, wantSub: "Transactions"},
		{name: "courier domain shipping", in: Input{FromEmail: "info@tcs.com.pk", Subject: "Parcel delivered", BodyHead: "Your parcel was delivered today."}, wantCat: CategoryAutomated, wantSub: "Shipping"},
		{name: "airline domain travel", in: Input{FromEmail: "info@pia.com.pk", Subject: "Special fares", BodyHead: "Fly to Dubai from Rs 45,000."}, wantCat: CategoryAutomated, wantSub: "Travel"},
		{name: "social domain", in: Input{FromEmail: "notification@facebook.com", Subject: "Ali mentioned you", BodyHead: "Ali mentioned you in a comment."}, wantCat: CategoryAutomated, wantSub: "Social"},
		{name: "shopping domain orders", in: Input{FromEmail: "orders@amazon.com", Subject: "Your order has shipped", BodyHead: "Your order has shipped and is on its way."}, wantCat: CategoryAutomated, wantSub: "Orders"},
		{name: "booking domain travel", in: Input{FromEmail: "noreply@booking.com", Subject: "Your stay is coming up", BodyHead: "Your hotel booking starts soon."}, wantCat: CategoryAutomated, wantSub: "Travel"},
		{name: "subdomain of bank", in: Input{FromEmail: "alerts@mail.nbp.com.pk", Subject: "Statement ready", BodyHead: "Your statement is ready."}, wantCat: CategoryAutomated, wantSub: "Transactions"},

		// --- Categories: hard signals ---
		{name: "list-unsubscribe forces automated", in: Input{FromEmail: "hello@someshop.example", Subject: "20% off everything", ListUnsubscribe: "<mailto:unsubscribe@someshop.example>", BodyHead: "Big sale this weekend."}, wantCat: CategoryAutomated, wantSub: "Promotions"},
		{name: "precedence bulk forces automated", in: Input{FromEmail: "team@someshop.example", Subject: "Weekly deals", Precedence: "bulk", BodyHead: "Deals of the week."}, wantCat: CategoryAutomated},
		{name: "noreply forces automated", in: Input{FromEmail: "noreply@random.example", Subject: "Hello", BodyHead: "Just saying hello."}, wantCat: CategoryAutomated},
		{name: "newsletter with year", in: Input{FromEmail: "news@substack.example", Subject: "2026 in review", ListUnsubscribe: "<https://substack.example/unsubscribe>", BodyHead: "The year 2026 was remarkable."}, wantCat: CategoryAutomated, wantKind: "", wantValue: ""},

		// --- Categories: personal ---
		{name: "plain personal mail", in: Input{FromEmail: "sara@example.com", Subject: "Dinner on Friday?", BodyHead: "Are you free for dinner on Friday? Let me know."}, wantCat: CategoryPersonal, wantSub: ""},
		{name: "known personal sender wins over keywords", in: Input{FromEmail: "sara@example.com", Subject: "Order of events", BodyHead: "Here is the order of events for the wedding.", KnownPersonalSender: true}, wantCat: CategoryPersonal, wantSub: ""},
		{name: "personal mail with digits", in: Input{FromEmail: "bilal@example.com", Subject: "Call me", BodyHead: "Call me on 0300 1234567 when you are free."}, wantCat: CategoryPersonal, wantSub: "", wantKind: "", wantValue: ""},
		{name: "personal mail mentioning code", in: Input{FromEmail: "sara@example.com", Subject: "The code", BodyHead: "The code for the gate is 4826, don't forget."}, wantCat: CategoryPersonal, wantSub: "", wantKind: "", wantValue: ""},
		{name: "roman urdu personal", in: Input{FromEmail: "ahmed@example.com", Subject: "Kal milte hain", BodyHead: "Kal shaam ko milte hain, chai peete hain."}, wantCat: CategoryPersonal, wantSub: ""},

		// --- Overrides ---
		{name: "override to personal", in: Input{FromEmail: "alerts@nbp.com.pk", Subject: "OTP 482619", BodyHead: "Your OTP is 482619.", OverrideCategory: CategoryPersonal, HasOverride: true}, wantCat: CategoryPersonal, wantSub: "", wantKind: "", wantValue: ""},
		{name: "override to automated", in: Input{FromEmail: "sara@example.com", Subject: "Hello", BodyHead: "Hi there.", OverrideCategory: CategoryAutomated, HasOverride: true}, wantCat: CategoryAutomated},
		{name: "override automated still classifies sub", in: Input{FromEmail: "sara@example.com", Subject: "Flight delayed", BodyHead: "Your flight is delayed.", OverrideCategory: CategoryAutomated, HasOverride: true}, wantCat: CategoryAutomated, wantSub: "Travel"},

		// --- Keywords ---
		{name: "keyword shipping", in: Input{FromEmail: "hello@someshop.example", Subject: "Your parcel is out for delivery", BodyHead: "It arrives today."}, wantCat: CategoryAutomated, wantSub: "Shipping"},
		{name: "keyword travel", in: Input{FromEmail: "hello@someshop.example", Subject: "Your boarding pass", BodyHead: "Show this at the gate."}, wantCat: CategoryAutomated, wantSub: "Travel"},
		{name: "keyword promotions unsubscribe", in: Input{FromEmail: "deals@someshop.example", Subject: "Big sale", BodyHead: "Click here to unsubscribe from these emails."}, wantCat: CategoryAutomated, wantSub: "Promotions"},
		{name: "keyword transactions", in: Input{FromEmail: "hello@somebank.example", Subject: "Transaction alert", BodyHead: "A transaction of Rs 500 was made."}, wantCat: CategoryAutomated, wantSub: "Transactions"},
		{name: "keyword social", in: Input{FromEmail: "hello@somenetwork.example", Subject: "You have a new follower", BodyHead: "Ali started following you."}, wantCat: CategoryAutomated, wantSub: "Social"},
		{name: "keyword orders", in: Input{FromEmail: "hello@someshop.example", Subject: "Order confirmation", BodyHead: "Thanks for your order."}, wantCat: CategoryAutomated, wantSub: "Orders"},

		// --- Extraction edge cases ---
		{name: "code next to order total", in: Input{FromEmail: "noreply@daraz.pk", Subject: "Order confirmed", BodyHead: "Your order total is Rs 2,499. Your verification code is 641208."}, wantCat: CategoryAutomated, wantSub: "Orders", wantKind: ExtractCode, wantValue: "641208"},
		{name: "pin inside shipping word is not a keyword", in: Input{FromEmail: "info@tcs.com.pk", Subject: "Shipping update", BodyHead: "Your parcel is shipping today, expected 2026-11-20."}, wantCat: CategoryAutomated, wantSub: "Shipping", wantKind: "", wantValue: ""},
		{name: "four digit code with leading zero", in: Input{FromEmail: "alerts@nbp.com.pk", Subject: "OTP", BodyHead: "Your OTP is 0482."}, wantCat: CategoryAutomated, wantSub: "Transactions", wantKind: ExtractCode, wantValue: "0482"},
		{name: "code in subject beats body code", in: Input{FromEmail: "alerts@nbp.com.pk", Subject: "Your OTP is 111222", BodyHead: "Your OTP is 111222. Old code was 999888."}, wantCat: CategoryAutomated, wantSub: "Transactions", wantKind: ExtractCode, wantValue: "111222"},
		{name: "reference without dashes", in: Input{FromEmail: "noreply@airblue.com", Subject: "Booking confirmed", BodyHead: "Your booking reference is AB472910. Thank you."}, wantCat: CategoryAutomated, wantSub: "Travel", wantKind: ExtractReference, wantValue: "AB472910"},
		{name: "gmail style code", in: Input{FromEmail: "no-reply@accounts.google.com", Subject: "Your Google verification code is 482619", BodyHead: "Your Google verification code is 482619."}, wantCat: CategoryAutomated, wantSub: "Transactions", wantKind: ExtractCode, wantValue: "482619"},
		{name: "whatsapp code", in: Input{FromEmail: "noreply@whatsapp.com", Subject: "WhatsApp code 482-619", BodyHead: "Your WhatsApp code is 482-619."}, wantCat: CategoryAutomated, wantSub: "Social", wantKind: "", wantValue: ""},
		{name: "empty message", in: Input{FromEmail: "x@example.com", Subject: "", BodyHead: ""}, wantCat: CategoryPersonal, wantSub: "", wantKind: "", wantValue: ""},
		{name: "automated local part with dash suffix", in: Input{FromEmail: "noreply-orders@someshop.example", Subject: "Hi", BodyHead: "Hi."}, wantCat: CategoryAutomated},
		{name: "do-not-reply", in: Input{FromEmail: "do-not-reply@somebank.example", Subject: "Alert", BodyHead: "Alert."}, wantCat: CategoryAutomated},
		{name: "support is not automated by itself", in: Input{FromEmail: "support@someshop.example", Subject: "Re: your question", BodyHead: "Thanks for writing in, we will help."}, wantCat: CategoryPersonal},
	}

	failed := 0
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Enrich(tc.in)
			if got.Category != tc.wantCat {
				t.Errorf("category = %q, want %q", got.Category, tc.wantCat)
				failed++
			}
			if got.SubCategory != tc.wantSub {
				t.Errorf("sub_category = %q, want %q", got.SubCategory, tc.wantSub)
			}
			if got.Kind() != tc.wantKind {
				t.Errorf("extract_kind = %q, want %q", got.Kind(), tc.wantKind)
			}
			if got.ExtractValue != tc.wantValue {
				t.Errorf("extract_value = %q, want %q", got.ExtractValue, tc.wantValue)
			}
			if tc.wantRuleHas != "" && !strings.Contains(got.ExtractRule, tc.wantRuleHas) {
				t.Errorf("extract_rule = %q, want it to contain %q", got.ExtractRule, tc.wantRuleHas)
			}
		})
	}
	_ = failed
}

// Kind returns the extract kind ("" when nothing was extracted).
func (r Result) Kind() string { return r.ExtractKind }

func TestParseListUnsubscribe(t *testing.T) {
	v, oneClick := ParseListUnsubscribe("<mailto:unsub@example.com>, <https://example.com/unsub>")
	if v == "" || !oneClick {
		t.Errorf("ParseListUnsubscribe = (%q, %v), want value + one-click URI", v, oneClick)
	}
	if v, _ := ParseListUnsubscribe(""); v != "" {
		t.Errorf("empty header should parse empty, got %q", v)
	}
}

func TestRedaction(t *testing.T) {
	r := Enrich(Input{FromEmail: "alerts@nbp.com.pk", Subject: "Your NBP OTP is 482619", BodyHead: "Your one-time password is 482619."})
	if r.ExtractValue != "482619" {
		t.Fatalf("fixture did not extract, got %+v", r)
	}
	// The safe log line must never contain the code.
	line := FormatSafeLog(r)
	if strings.Contains(line, "482619") {
		t.Errorf("safe log line contains the extracted code: %q", line)
	}
	for k, v := range SafeLogFields(r) {
		if s, ok := v.(string); ok && strings.Contains(s, "482619") {
			t.Errorf("SafeLogFields[%s] contains the code", k)
		}
	}
	// RedactValue strips the code from free text.
	redacted := RedactValue("Your OTP is 482619, use it now 482619.", "482619")
	if strings.Contains(redacted, "482619") {
		t.Errorf("RedactValue did not strip the code: %q", redacted)
	}
	if !strings.Contains(redacted, "[redacted]") {
		t.Errorf("RedactValue should mark the redaction: %q", redacted)
	}
}

func TestAttentionEvaluation(t *testing.T) {
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)

	// 1. Verification code, fresh.
	if item, ok := EvaluateAttention(AttentionInput{ExtractKind: ExtractCode, Date: now.Add(-1 * time.Hour)}, now); !ok || item.Type != AttentionVerificationCode {
		t.Errorf("fresh code should qualify as verification_code, got %+v %v", item, ok)
	}
	// 1b. Verification code, stale (>24h).
	if _, ok := EvaluateAttention(AttentionInput{ExtractKind: ExtractCode, Date: now.Add(-25 * time.Hour)}, now); ok {
		t.Errorf("25h-old code should have expired")
	}
	// 2. Flight changed.
	if item, ok := EvaluateAttention(AttentionInput{SubCategory: "Travel", FromEmail: "noreply@pia.com.pk", Subject: "Your flight is cancelled", Date: now.Add(-2 * time.Hour)}, now); !ok || item.Type != AttentionFlightChanged {
		t.Errorf("cancelled flight should qualify, got %+v %v", item, ok)
	}
	// 2b. Travel mail without change keywords: no bar.
	if _, ok := EvaluateAttention(AttentionInput{SubCategory: "Travel", FromEmail: "noreply@pia.com.pk", Subject: "Your boarding pass", Date: now.Add(-2 * time.Hour)}, now); ok {
		t.Errorf("boarding pass without change keywords should not qualify")
	}
	// 2c. Flight changed expires after travel date.
	if _, ok := EvaluateAttention(AttentionInput{SubCategory: "Travel", FromEmail: "noreply@pia.com.pk", Subject: "Flight cancelled on 10 Nov 2026", BodyHead: "Your flight on 10 Nov 2026 is cancelled.", Date: now.Add(-2 * time.Hour)}, now); ok {
		t.Errorf("flight changed should expire after the (past) travel date")
	}
	// 3. Payment failed, 14-day window.
	if item, ok := EvaluateAttention(AttentionInput{SubCategory: "Transactions", FromEmail: "alerts@hbl.com", Subject: "Payment failed", Date: now.Add(-13 * 24 * time.Hour)}, now); !ok || item.Type != AttentionPaymentFailed {
		t.Errorf("13-day-old payment failure should qualify, got %+v %v", item, ok)
	}
	if _, ok := EvaluateAttention(AttentionInput{SubCategory: "Transactions", FromEmail: "alerts@hbl.com", Subject: "Payment failed", Date: now.Add(-15 * 24 * time.Hour)}, now); ok {
		t.Errorf("15-day-old payment failure should have expired")
	}
	// 4. Delivery problem, 7-day window.
	if item, ok := EvaluateAttention(AttentionInput{SubCategory: "Shipping", FromEmail: "noreply@tcs.com.pk", Subject: "Delivery exception", BodyHead: "Your parcel is held at customs.", Date: now.Add(-6 * 24 * time.Hour)}, now); !ok || item.Type != AttentionDeliveryProblem {
		t.Errorf("6-day-old delivery problem should qualify, got %+v %v", item, ok)
	}
	if _, ok := EvaluateAttention(AttentionInput{SubCategory: "Shipping", FromEmail: "noreply@tcs.com.pk", Subject: "Delivery exception", Date: now.Add(-8 * 24 * time.Hour)}, now); ok {
		t.Errorf("8-day-old delivery problem should have expired")
	}
	// Security alerts are NOT in v1.
	if _, ok := EvaluateAttention(AttentionInput{SubCategory: "Updates", FromEmail: "no-reply@google.com", Subject: "Security alert: new sign-in", BodyHead: "Someone signed in from a new device.", Date: now.Add(-1 * time.Hour)}, now); ok {
		t.Errorf("security alerts are explicitly not in v1")
	}
	// Code beats other rules: a code mail with travel keywords is a code bar.
	if item, ok := EvaluateAttention(AttentionInput{ExtractKind: ExtractCode, SubCategory: "Travel", Subject: "Flight cancelled, code 482619", Date: now.Add(-1 * time.Hour)}, now); !ok || item.Type != AttentionVerificationCode {
		t.Errorf("code should win over flight rule, got %+v %v", item, ok)
	}
}

// TestEnrich_KnownPersonalSenderBeatsHardSignals (fourth-audit finding
// 40): a known personal sender's mail stays Personal even with a
// List-Unsubscribe header, a bulk Precedence, an automated-looking
// local part, or a forced-Automated verdict from an earlier pass —
// but an explicit sender override to Automated still wins.
func TestEnrich_KnownPersonalSenderBeatsHardSignals(t *testing.T) {
	base := Input{
		FromEmail:           "sara@example.com",
		Subject:             "Hello",
		KnownPersonalSender: true,
	}
	cases := []struct {
		name string
		in   Input
	}{
		{"list-unsubscribe header", Input{FromEmail: base.FromEmail, Subject: base.Subject, ListUnsubscribe: "<https://example.com/unsub>", KnownPersonalSender: true}},
		{"bulk precedence", Input{FromEmail: base.FromEmail, Subject: base.Subject, Precedence: "bulk", KnownPersonalSender: true}},
		{"automated local part", Input{FromEmail: "noreply@example.com", Subject: base.Subject, KnownPersonalSender: true}},
		{"forced automated (earlier pass)", Input{FromEmail: base.FromEmail, Subject: base.Subject, ListUnsubscribe: "<https://example.com/unsub>", ForceAutomated: true, KnownPersonalSender: true}},
	}
	for _, c := range cases {
		if got := Enrich(c.in); got.Category != CategoryPersonal {
			t.Errorf("%s: category = %q, want Personal", c.name, got.Category)
		}
	}
	// Sanity: without the known-personal flag the same mail is Automated.
	if got := Enrich(Input{FromEmail: base.FromEmail, Subject: base.Subject, ListUnsubscribe: "<https://example.com/unsub>"}); got.Category != CategoryAutomated {
		t.Errorf("no known-personal flag: category = %q, want Automated", got.Category)
	}
	// Explicit override to Automated still wins over known-personal.
	if got := Enrich(Input{FromEmail: base.FromEmail, Subject: base.Subject, HasOverride: true, OverrideCategory: CategoryAutomated, KnownPersonalSender: true}); got.Category != CategoryAutomated {
		t.Errorf("explicit Automated override: category = %q, want Automated", got.Category)
	}
}
