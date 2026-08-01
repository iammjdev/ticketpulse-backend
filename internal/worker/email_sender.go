package worker

import (
	"fmt"
	"log"
	"net/smtp"
	"strings"

	"github.com/iammjdev/ticketpulse-backend/internal/config"
)

// SendETicketEmail delivers the rendered e-ticket via SMTP when cfg.Enabled, otherwise logs a
// clean structured block to stdout — the documented dev fallback for running without a mail
// server configured.
func SendETicketEmail(cfg config.SMTPConfig, to, subject, htmlBody string, data TicketEmailData) error {
	if !cfg.Enabled {
		logETicketFallback(to, subject, data)
		return nil
	}

	msg := buildMIMEMessage(cfg.From, to, subject, htmlBody)

	addr := fmt.Sprintf("%s:%s", cfg.Host, cfg.Port)
	var auth smtp.Auth
	if cfg.Username != "" {
		auth = smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.Host)
	}

	if err := smtp.SendMail(addr, auth, cfg.From, []string{to}, msg); err != nil {
		return fmt.Errorf("failed to send e-ticket email via SMTP (%s): %w", addr, err)
	}

	log.Printf("📧 E-ticket email sent to %s for order %s\n", to, data.OrderID)
	return nil
}

func buildMIMEMessage(from, to, subject, htmlBody string) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "From: TicketPulse <%s>\r\n", from)
	fmt.Fprintf(&b, "To: %s\r\n", to)
	fmt.Fprintf(&b, "Subject: %s\r\n", subject)
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/html; charset=UTF-8\r\n")
	b.WriteString("\r\n")
	b.WriteString(htmlBody)
	return []byte(b.String())
}

// logETicketFallback prints a clean, structured e-ticket summary to stdout — used whenever
// SMTP_HOST isn't configured (default in dev), so the full notification pipeline is still
// observable end to end without standing up a mail server.
func logETicketFallback(to, subject string, data TicketEmailData) {
	log.Printf(
		"\n"+
			"┌─────────────────────────────────────────────────────────\n"+
			"│ 📧 E-TICKET EMAIL (SMTP not configured — logged instead)\n"+
			"├─────────────────────────────────────────────────────────\n"+
			"│ To:        %s\n"+
			"│ Subject:   %s\n"+
			"│ Order:     %s\n"+
			"│ Event:     %s\n"+
			"│ Venue:     %s\n"+
			"│ Date:      %s\n"+
			"│ Zone/Qty:  %s x%d\n"+
			"│ Amount:    %s THB\n"+
			"│ Signature: %s\n"+
			"└─────────────────────────────────────────────────────────\n",
		to, subject, data.OrderID, data.EventTitle, data.VenueName, data.EventDate,
		data.ZoneName, data.Quantity, data.TotalAmount, data.Signature,
	)
}
