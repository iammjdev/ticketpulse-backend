package service

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log"
	"math/big"
	"net/smtp"
	"os"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/redis/go-redis/v9"
	"golang.org/x/crypto/bcrypt"

	"github.com/iammjdev/ticketpulse-backend/internal/domain"
	"github.com/iammjdev/ticketpulse-backend/internal/repository"
)

const defaultJWTSecret = "ticketpulse_super_secret_jwt_key_2026"

const (
	otpTTL         = 5 * time.Minute
	otpCooldownTTL = 60 * time.Second
	otpKeyPrefix   = "otp:"
	cooldownPrefix = "otp_cooldown:"
)

var (
	ErrEmailTaken             = errors.New("an account with this email already exists")
	ErrInvalidCredentials     = errors.New("invalid email or password")
	ErrEmailNotVerified       = errors.New("email not verified")
	ErrInvalidOrExpiredOTP    = errors.New("invalid or expired otp")
	ErrResendCooldown         = errors.New("please wait before requesting another code")
	ErrInvalidCurrentPassword = errors.New("current password is incorrect")
)

func jwtSecret() []byte {
	if s := os.Getenv("JWT_SECRET"); s != "" {
		return []byte(s)
	}
	return []byte(defaultJWTSecret)
}

type Claims struct {
	Email string          `json:"email"`
	Role  domain.UserRole `json:"role"`
	jwt.RegisteredClaims
}

type AuthService struct {
	users repository.UserRepository
	redis *redis.Client
}

func NewAuthService(users repository.UserRepository, redisClient *redis.Client) *AuthService {
	return &AuthService{users: users, redis: redisClient}
}

// GenerateOTP returns a cryptographically random 6-digit numeric code, zero-padded (e.g. "004829").
func GenerateOTP() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

// SendOTPEmail delivers the OTP via SMTP when SMTP_HOST is configured; otherwise it logs the
// code to stdout so local development can proceed without a mail server.
func SendOTPEmail(email, otp string) error {
	host := os.Getenv("SMTP_HOST")
	if host == "" {
		log.Printf("📧 [DEV] OTP for %s: %s (expires in 5 minutes)\n", email, otp)
		return nil
	}

	port := os.Getenv("SMTP_PORT")
	from := os.Getenv("SMTP_FROM")
	user := os.Getenv("SMTP_USER")
	pass := os.Getenv("SMTP_PASSWORD")

	subject := "Your TicketPulse verification code"
	body := fmt.Sprintf("Your TicketPulse verification code is %s. It expires in 5 minutes.", otp)
	msg := []byte(fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\n\r\n%s\r\n", from, email, subject, body))

	auth := smtp.PlainAuth("", user, pass, host)
	if err := smtp.SendMail(host+":"+port, auth, from, []string{email}, msg); err != nil {
		log.Printf("⚠️ Failed to send OTP email to %s via SMTP: %v\n", email, err)
		return err
	}
	return nil
}

// Register creates (or re-issues an OTP for) an unverified account. Verified emails are
// rejected with ErrEmailTaken; unverified re-registrations refresh the password + OTP.
func (s *AuthService) Register(ctx context.Context, email, password string) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), 10)
	if err != nil {
		return err
	}

	existing, err := s.users.FindByEmail(ctx, email)
	if err != nil && !errors.Is(err, repository.ErrUserNotFound) {
		return err
	}

	if existing != nil {
		if existing.IsVerified {
			return ErrEmailTaken
		}
		if err := s.users.UpdatePasswordHash(ctx, existing.ID, string(hash)); err != nil {
			return err
		}
	} else {
		user := &domain.User{
			Email:        email,
			PasswordHash: string(hash),
			Role:         domain.RoleUser,
			MemberTier:   "REGULAR",
			IsVerified:   false,
		}
		if err := s.users.CreateUser(ctx, user); err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23505" {
				return ErrEmailTaken
			}
			return err
		}
	}

	return s.issueOTP(ctx, email)
}

// VerifyOTP checks the submitted code against Redis, marks the user verified, and issues a
// signed session token on success.
func (s *AuthService) VerifyOTP(ctx context.Context, email, otp string) (*domain.User, string, error) {
	stored, err := s.redis.Get(ctx, otpKeyPrefix+email).Result()
	if err != nil || stored != otp {
		return nil, "", ErrInvalidOrExpiredOTP
	}

	user, err := s.users.MarkVerified(ctx, email)
	if err != nil {
		return nil, "", err
	}

	s.redis.Del(ctx, otpKeyPrefix+email, cooldownPrefix+email)

	token, err := generateToken(user)
	if err != nil {
		return nil, "", err
	}
	return user, token, nil
}

// ResendOTP re-issues a code, gated by a 60-second cooldown per email.
func (s *AuthService) ResendOTP(ctx context.Context, email string) error {
	exists, err := s.redis.Exists(ctx, cooldownPrefix+email).Result()
	if err != nil {
		return err
	}
	if exists > 0 {
		return ErrResendCooldown
	}

	user, err := s.users.FindByEmail(ctx, email)
	if err != nil {
		return err
	}
	if user.IsVerified {
		return ErrEmailTaken
	}

	if err := s.issueOTP(ctx, email); err != nil {
		return err
	}
	return s.redis.Set(ctx, cooldownPrefix+email, "1", otpCooldownTTL).Err()
}

func (s *AuthService) issueOTP(ctx context.Context, email string) error {
	otp, err := GenerateOTP()
	if err != nil {
		return err
	}
	if err := s.redis.Set(ctx, otpKeyPrefix+email, otp, otpTTL).Err(); err != nil {
		return err
	}
	return SendOTPEmail(email, otp)
}

func (s *AuthService) Login(ctx context.Context, email, password string) (*domain.User, string, error) {
	user, err := s.users.FindByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, repository.ErrUserNotFound) {
			return nil, "", ErrInvalidCredentials
		}
		return nil, "", err
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)); err != nil {
		return nil, "", ErrInvalidCredentials
	}

	if !user.IsVerified {
		return nil, "", ErrEmailNotVerified
	}

	token, err := generateToken(user)
	if err != nil {
		return nil, "", err
	}
	return user, token, nil
}

func (s *AuthService) GetProfile(ctx context.Context, userID string) (*domain.User, error) {
	return s.users.FindByID(ctx, userID)
}

func (s *AuthService) UpdateProfile(ctx context.Context, userID, fullName, phone, nationalID string) (*domain.User, error) {
	return s.users.UpdateProfile(ctx, userID, fullName, phone, nationalID)
}

// ChangePassword verifies currentPassword against the stored hash before committing newPassword.
func (s *AuthService) ChangePassword(ctx context.Context, userID, currentPassword, newPassword string) error {
	user, err := s.users.FindByID(ctx, userID)
	if err != nil {
		return err
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(currentPassword)); err != nil {
		return ErrInvalidCurrentPassword
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), 10)
	if err != nil {
		return err
	}

	return s.users.UpdatePasswordHash(ctx, userID, string(hash))
}

func generateToken(user *domain.User) (string, error) {
	claims := Claims{
		Email: user.Email,
		Role:  user.Role,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   user.ID,
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(24 * time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(jwtSecret())
}

// ParseToken validates a JWT and returns its claims. Shared with the auth middleware
// so token verification stays in one place.
func ParseToken(tokenString string) (*Claims, error) {
	claims := &Claims{}
	token, err := jwt.ParseWithClaims(tokenString, claims, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, jwt.ErrSignatureInvalid
		}
		return jwtSecret(), nil
	})
	if err != nil || !token.Valid {
		return nil, errors.New("invalid or expired token")
	}
	return claims, nil
}
