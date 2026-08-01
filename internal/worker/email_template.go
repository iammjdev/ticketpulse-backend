package worker

import (
	"bytes"
	"fmt"
	"html/template"
)

// TicketEmailData feeds the e-ticket HTML template. All fields are pre-formatted strings —
// formatting (dates, currency) happens in notification_worker.go so the template stays a pure
// presentation layer.
type TicketEmailData struct {
	RecipientName string
	OrderID       string
	EventTitle    string
	VenueName     string
	EventDate     string
	ZoneName      string
	Quantity      int
	TotalAmount   string
	QRCodeURL     string
	Signature     string
}

// eTicketHTMLTemplate is a table-based HTML email layout (inline styles only) for maximum
// client compatibility (Gmail/Outlook strip <style> blocks in many contexts).
const eTicketHTMLTemplate = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Your TicketPulse E-Ticket</title>
</head>
<body style="margin:0;padding:0;background-color:#0b0b0f;font-family:-apple-system,Segoe UI,Roboto,Helvetica,Arial,sans-serif;">
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="background-color:#0b0b0f;padding:32px 16px;">
  <tr>
    <td align="center">
      <table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="max-width:600px;background-color:#15151c;border-radius:24px;overflow:hidden;border:1px solid #2a2a35;">

        <!-- Brand Header -->
        <tr>
          <td style="background:linear-gradient(90deg,#6d5efc,#8f5efc);padding:28px 32px;">
            <table role="presentation" width="100%" cellpadding="0" cellspacing="0">
              <tr>
                <td style="font-size:20px;font-weight:900;color:#ffffff;letter-spacing:-0.5px;">TicketPulse</td>
                <td align="right">
                  <span style="display:inline-block;background:rgba(255,255,255,0.18);color:#ffffff;font-size:10px;font-weight:700;letter-spacing:1px;text-transform:uppercase;padding:6px 12px;border-radius:999px;border:1px solid rgba(255,255,255,0.3);">E-Ticket Confirmed</span>
                </td>
              </tr>
            </table>
          </td>
        </tr>

        <!-- Greeting -->
        <tr>
          <td style="padding:28px 32px 0 32px;">
            <p style="margin:0;color:#9a9aab;font-size:13px;">Hi {{.RecipientName}},</p>
            <p style="margin:8px 0 0 0;color:#f4f4f7;font-size:15px;font-weight:600;">Your payment was successful — here is your official entry pass.</p>
          </td>
        </tr>

        <!-- Event Card -->
        <tr>
          <td style="padding:24px 32px 0 32px;">
            <table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="background-color:#1d1d27;border:1px solid #2a2a35;border-radius:18px;">
              <tr>
                <td style="padding:24px;">
                  <p style="margin:0 0 4px 0;color:#f4f4f7;font-size:19px;font-weight:800;line-height:1.3;">{{.EventTitle}}</p>
                  <p style="margin:0;color:#9a9aab;font-size:13px;">{{.VenueName}}</p>

                  <table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="margin-top:20px;">
                    <tr>
                      <td width="50%" style="padding-bottom:16px;vertical-align:top;">
                        <p style="margin:0 0 2px 0;color:#6d6d80;font-size:10px;font-weight:700;letter-spacing:0.5px;text-transform:uppercase;">Date &amp; Time</p>
                        <p style="margin:0;color:#f4f4f7;font-size:13px;font-weight:600;">{{.EventDate}}</p>
                      </td>
                      <td width="50%" style="padding-bottom:16px;vertical-align:top;">
                        <p style="margin:0 0 2px 0;color:#6d6d80;font-size:10px;font-weight:700;letter-spacing:0.5px;text-transform:uppercase;">Zone / Qty</p>
                        <p style="margin:0;color:#f4f4f7;font-size:13px;font-weight:600;">{{.ZoneName}} &times; {{.Quantity}}</p>
                      </td>
                    </tr>
                    <tr>
                      <td width="50%" style="vertical-align:top;">
                        <p style="margin:0 0 2px 0;color:#6d6d80;font-size:10px;font-weight:700;letter-spacing:0.5px;text-transform:uppercase;">Amount Paid</p>
                        <p style="margin:0;color:#4ade80;font-size:13px;font-weight:700;">{{.TotalAmount}} THB</p>
                      </td>
                      <td width="50%" style="vertical-align:top;">
                        <p style="margin:0 0 2px 0;color:#6d6d80;font-size:10px;font-weight:700;letter-spacing:0.5px;text-transform:uppercase;">Order Ref</p>
                        <p style="margin:0;color:#f4f4f7;font-size:12px;font-weight:600;font-family:monospace;">{{.OrderID}}</p>
                      </td>
                    </tr>
                  </table>
                </td>
              </tr>
            </table>
          </td>
        </tr>

        <!-- QR Code -->
        <tr>
          <td style="padding:24px 32px 0 32px;" align="center">
            <table role="presentation" cellpadding="0" cellspacing="0" style="background-color:#ffffff;border-radius:18px;">
              <tr>
                <td style="padding:20px;" align="center">
                  <img src="{{.QRCodeURL}}" width="180" height="180" alt="Gate entry QR code" style="display:block;border:0;width:180px;height:180px;">
                </td>
              </tr>
            </table>
            <p style="margin:12px 0 0 0;color:#6d6d80;font-size:10px;font-family:monospace;">HMAC-signed &middot; scan at the gate for entry</p>
          </td>
        </tr>

        <!-- Footer -->
        <tr>
          <td style="padding:28px 32px 32px 32px;">
            <table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="border-top:1px solid #2a2a35;padding-top:20px;">
              <tr>
                <td style="padding-top:20px;">
                  <p style="margin:0;color:#6d6d80;font-size:11px;line-height:1.6;">Present this QR code at the venue gate. This pass is unique to your order — do not share it. Need help? Reply to this email or visit your TicketPulse account under My Tickets.</p>
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

var parsedETicketTemplate = template.Must(template.New("eticket").Parse(eTicketHTMLTemplate))

// RenderETicketEmail renders the subject line and HTML body for an e-ticket confirmation.
func RenderETicketEmail(data TicketEmailData) (subject string, htmlBody string, err error) {
	var buf bytes.Buffer
	if err := parsedETicketTemplate.Execute(&buf, data); err != nil {
		return "", "", fmt.Errorf("failed to render e-ticket email template: %w", err)
	}
	subject = fmt.Sprintf("Your TicketPulse E-Ticket — %s", data.EventTitle)
	return subject, buf.String(), nil
}
