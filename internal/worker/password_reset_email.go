package worker

import (
	"bytes"
	"fmt"
	"html/template"
	"log"
	"net/smtp"

	"github.com/iammjdev/ticketpulse-backend/internal/config"
)

// PasswordResetEmailData feeds the password reset HTML template.
type PasswordResetEmailData struct {
	RecipientName string
	Token         string
	ExpiresInMins int
}

const passwordResetHTMLTemplate = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>TicketPulse Password Reset</title>
</head>
<body style="margin:0;padding:0;background-color:#0b0b0f;font-family:-apple-system,Segoe UI,Roboto,Helvetica,Arial,sans-serif;">
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="background-color:#0b0b0f;padding:32px 16px;">
  <tr>
    <td align="center">
      <table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="max-width:600px;background-color:#15151c;border-radius:24px;overflow:hidden;border:1px solid #2a2a35;">

        <tr>
          <td style="background:linear-gradient(90deg,#6d5efc,#8f5efc);padding:28px 32px;">
            <table role="presentation" width="100%" cellpadding="0" cellspacing="0">
              <tr>
                <td style="font-size:20px;font-weight:900;color:#ffffff;letter-spacing:-0.5px;">TicketPulse</td>
                <td align="right">
                  <span style="display:inline-block;background:rgba(255,255,255,0.18);color:#ffffff;font-size:10px;font-weight:700;letter-spacing:1px;text-transform:uppercase;padding:6px 12px;border-radius:999px;border:1px solid rgba(255,255,255,0.3);">Password Reset</span>
                </td>
              </tr>
            </table>
          </td>
        </tr>

        <tr>
          <td style="padding:28px 32px 0 32px;">
            <p style="margin:0;color:#9a9aab;font-size:13px;">Hi {{.RecipientName}},</p>
            <p style="margin:8px 0 0 0;color:#f4f4f7;font-size:15px;font-weight:600;">An administrator requested a password reset for your TicketPulse account.</p>
          </td>
        </tr>

        <tr>
          <td style="padding:24px 32px 0 32px;">
            <table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="background-color:#1d1d27;border:1px solid #2a2a35;border-radius:18px;">
              <tr>
                <td style="padding:24px;" align="center">
                  <p style="margin:0 0 8px 0;color:#6d6d80;font-size:10px;font-weight:700;letter-spacing:0.5px;text-transform:uppercase;">Reset Reference Code</p>
                  <p style="margin:0;color:#f4f4f7;font-size:22px;font-weight:800;font-family:monospace;letter-spacing:2px;">{{.Token}}</p>
                  <p style="margin:12px 0 0 0;color:#6d6d80;font-size:11px;">Expires in {{.ExpiresInMins}} minutes</p>
                </td>
              </tr>
            </table>
          </td>
        </tr>

        <tr>
          <td style="padding:28px 32px 32px 32px;">
            <table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="border-top:1px solid #2a2a35;padding-top:20px;">
              <tr>
                <td style="padding-top:20px;">
                  <p style="margin:0;color:#6d6d80;font-size:11px;line-height:1.6;">If you did not expect this, contact support immediately — your account may be at risk. Quote the reference code above when contacting the TicketPulse support team.</p>
                </td>
              </tr>
            </table>
          </td>
        </tr>

      </table>
    </td>
  </tr>
</table>
</body>
</html>`

var parsedPasswordResetTemplate = template.Must(template.New("password_reset").Parse(passwordResetHTMLTemplate))

// RenderPasswordResetEmail renders the subject line and HTML body for an admin-triggered
// password reset notice.
func RenderPasswordResetEmail(data PasswordResetEmailData) (subject string, htmlBody string, err error) {
	var buf bytes.Buffer
	if err := parsedPasswordResetTemplate.Execute(&buf, data); err != nil {
		return "", "", fmt.Errorf("failed to render password reset email template: %w", err)
	}
	return "TicketPulse Password Reset Requested", buf.String(), nil
}

// SendPasswordResetEmail delivers the rendered password reset notice via SMTP when
// cfg.Enabled, otherwise logs a clean structured block to stdout — the same dev fallback
// pattern as SendETicketEmail.
func SendPasswordResetEmail(cfg config.SMTPConfig, to, subject, htmlBody string, data PasswordResetEmailData) error {
	if !cfg.Enabled {
		logPasswordResetFallback(to, subject, data)
		return nil
	}

	msg := buildMIMEMessage(cfg.From, to, subject, htmlBody)

	addr := fmt.Sprintf("%s:%s", cfg.Host, cfg.Port)
	var auth smtp.Auth
	if cfg.Username != "" {
		auth = smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.Host)
	}

	if err := smtp.SendMail(addr, auth, cfg.From, []string{to}, msg); err != nil {
		return fmt.Errorf("failed to send password reset email via SMTP (%s): %w", addr, err)
	}

	log.Printf("📧 Password reset email sent to %s\n", to)
	return nil
}

func logPasswordResetFallback(to, subject string, data PasswordResetEmailData) {
	log.Printf(
		"\n"+
			"┌─────────────────────────────────────────────────────────\n"+
			"│ 🔑 PASSWORD RESET EMAIL (SMTP not configured — logged instead)\n"+
			"├─────────────────────────────────────────────────────────\n"+
			"│ To:      %s\n"+
			"│ Subject: %s\n"+
			"│ Token:   %s\n"+
			"│ Expires: %d minutes\n"+
			"└─────────────────────────────────────────────────────────\n",
		to, subject, data.Token, data.ExpiresInMins,
	)
}
