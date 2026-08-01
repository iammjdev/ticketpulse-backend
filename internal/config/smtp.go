package config

import "os"

// SMTPConfig configures outbound e-ticket email delivery. Enabled is false whenever SMTP_HOST
// is unset — the notification worker falls back to logging a structured e-ticket block to
// stdout instead of failing, so local dev works without a mail server.
type SMTPConfig struct {
	Host     string
	Port     string
	Username string
	Password string
	From     string
	Enabled  bool
}

func LoadSMTPConfig() SMTPConfig {
	host := os.Getenv("SMTP_HOST")
	port := os.Getenv("SMTP_PORT")
	if port == "" {
		port = "587"
	}
	from := os.Getenv("SMTP_FROM")
	if from == "" {
		from = "no-reply@ticketpulse.com"
	}

	return SMTPConfig{
		Host:     host,
		Port:     port,
		Username: os.Getenv("SMTP_USERNAME"),
		Password: os.Getenv("SMTP_PASSWORD"),
		From:     from,
		Enabled:  host != "",
	}
}
