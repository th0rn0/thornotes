package config

import (
	"flag"
	"io"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func resetFlags() {
	flag.CommandLine = flag.NewFlagSet(os.Args[0], flag.ContinueOnError)
	flag.CommandLine.SetOutput(io.Discard)
}

func TestEnvOr_KeySet(t *testing.T) {
	t.Setenv("TEST_ENVKEY", "myvalue")
	result := envOr("TEST_ENVKEY", "default")
	assert.Equal(t, "myvalue", result)
}

func TestEnvOr_KeyNotSet(t *testing.T) {
	os.Unsetenv("TEST_ENVKEY_NOTSET")
	result := envOr("TEST_ENVKEY_NOTSET", "fallback")
	assert.Equal(t, "fallback", result)
}

func TestEnvBool_Empty_ReturnsDefault(t *testing.T) {
	os.Unsetenv("TEST_BOOL")
	assert.True(t, envBool("TEST_BOOL", true))
	assert.False(t, envBool("TEST_BOOL", false))
}

func TestEnvBool_True(t *testing.T) {
	t.Setenv("TEST_BOOL", "true")
	assert.True(t, envBool("TEST_BOOL", false))
}

func TestEnvBool_False(t *testing.T) {
	t.Setenv("TEST_BOOL", "false")
	assert.False(t, envBool("TEST_BOOL", true))
}

func TestEnvBool_One(t *testing.T) {
	t.Setenv("TEST_BOOL", "1")
	assert.True(t, envBool("TEST_BOOL", false))
}

func TestEnvBool_TRUEUppercase(t *testing.T) {
	t.Setenv("TEST_BOOL", "TRUE")
	assert.True(t, envBool("TEST_BOOL", false))
}

func TestParse_Defaults(t *testing.T) {
	resetFlags()
	os.Unsetenv("THORNOTES_ADDR")
	os.Unsetenv("THORNOTES_DB")
	os.Unsetenv("THORNOTES_NOTES_ROOT")
	os.Unsetenv("THORNOTES_ALLOW_REGISTRATION")
	os.Unsetenv("THORNOTES_TRUSTED_PROXY")

	cfg, err := Parse()
	require.NoError(t, err)
	assert.Equal(t, ":8080", cfg.Addr)
	assert.Equal(t, "thornotes.db", cfg.DBPath)
	assert.Equal(t, "notes", cfg.NotesRoot)
	assert.True(t, cfg.AllowRegistration)
	assert.Nil(t, cfg.TrustedProxy)
	assert.Equal(t, int64(1<<20), cfg.MaxContentBytes)
}

func TestParse_EnvOverridesAddr(t *testing.T) {
	resetFlags()
	t.Setenv("THORNOTES_ADDR", ":9090")
	os.Unsetenv("THORNOTES_DB")
	os.Unsetenv("THORNOTES_NOTES_ROOT")
	os.Unsetenv("THORNOTES_ALLOW_REGISTRATION")
	os.Unsetenv("THORNOTES_TRUSTED_PROXY")

	cfg, err := Parse()
	require.NoError(t, err)
	assert.Equal(t, ":9090", cfg.Addr)
}

func TestParse_ValidCIDR_SetsTrustedProxy(t *testing.T) {
	resetFlags()
	os.Unsetenv("THORNOTES_ADDR")
	os.Unsetenv("THORNOTES_DB")
	os.Unsetenv("THORNOTES_NOTES_ROOT")
	os.Unsetenv("THORNOTES_ALLOW_REGISTRATION")
	t.Setenv("THORNOTES_TRUSTED_PROXY", "10.0.0.0/8")

	cfg, err := Parse()
	require.NoError(t, err)
	require.NotNil(t, cfg.TrustedProxy)
	assert.Equal(t, "10.0.0.0/8", cfg.TrustedProxy.String())
}

func TestParse_InvalidCIDR_ReturnsError(t *testing.T) {
	resetFlags()
	os.Unsetenv("THORNOTES_ADDR")
	os.Unsetenv("THORNOTES_DB")
	os.Unsetenv("THORNOTES_NOTES_ROOT")
	os.Unsetenv("THORNOTES_ALLOW_REGISTRATION")
	t.Setenv("THORNOTES_TRUSTED_PROXY", "not-a-cidr")

	_, err := Parse()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid --trusted-proxy CIDR")
}

func TestParse_SkipReconciliation_Default(t *testing.T) {
	resetFlags()
	os.Unsetenv("THORNOTES_SKIP_RECONCILIATION")

	cfg, err := Parse()
	require.NoError(t, err)
	assert.False(t, cfg.SkipReconciliation)
}

func TestParse_SkipReconciliation_EnvTrue(t *testing.T) {
	resetFlags()
	t.Setenv("THORNOTES_SKIP_RECONCILIATION", "true")

	cfg, err := Parse()
	require.NoError(t, err)
	assert.True(t, cfg.SkipReconciliation)
}

func TestEnvDuration_NotSet_ReturnsDefault(t *testing.T) {
	os.Unsetenv("TEST_DURATION_UNSET")
	d := envDuration("TEST_DURATION_UNSET", 30*time.Second)
	assert.Equal(t, 30*time.Second, d)
}

func TestEnvDuration_ValidValue(t *testing.T) {
	t.Setenv("TEST_DURATION", "5m")
	d := envDuration("TEST_DURATION", 30*time.Second)
	assert.Equal(t, 5*time.Minute, d)
}

func TestEnvDuration_InvalidValue_ReturnsDefault(t *testing.T) {
	t.Setenv("TEST_DURATION_BAD", "notaduration")
	d := envDuration("TEST_DURATION_BAD", 30*time.Second)
	assert.Equal(t, 30*time.Second, d)
}

func TestEnvDuration_Zero(t *testing.T) {
	t.Setenv("TEST_DURATION_ZERO", "0")
	d := envDuration("TEST_DURATION_ZERO", 30*time.Second)
	assert.Equal(t, time.Duration(0), d)
}

func TestEnvInt_Unset_ReturnsDefault(t *testing.T) {
	os.Unsetenv("TEST_INT_UNSET")
	assert.Equal(t, 7, envInt("TEST_INT_UNSET", 7))
}

func TestEnvInt_Invalid_ReturnsDefault(t *testing.T) {
	t.Setenv("TEST_INT_BAD", "not-a-number")
	assert.Equal(t, 7, envInt("TEST_INT_BAD", 7))
}

func TestEnvInt_ValidValue(t *testing.T) {
	t.Setenv("TEST_INT", " 42 ")
	assert.Equal(t, 42, envInt("TEST_INT", 7))
}

// TestParse_ShareRateDefaults pins the default budgets the anonymous share
// surfaces ship with: generous enough for a human browsing a folder, bounded for
// someone looping one.
func TestParse_ShareRateDefaults(t *testing.T) {
	resetFlags()
	os.Unsetenv("THORNOTES_ADDR")
	os.Unsetenv("THORNOTES_TRUSTED_PROXY")
	os.Unsetenv("THORNOTES_SHARE_RATE_PER_MINUTE")
	os.Unsetenv("THORNOTES_SHARE_RATE_PER_HOUR")

	cfg, err := Parse()
	require.NoError(t, err)
	assert.Equal(t, 60, cfg.ShareRatePerMinute)
	assert.Equal(t, 600, cfg.ShareRatePerHour)
}

func TestParse_ShareRate_EnvOverrides(t *testing.T) {
	resetFlags()
	os.Unsetenv("THORNOTES_ADDR")
	os.Unsetenv("THORNOTES_TRUSTED_PROXY")
	t.Setenv("THORNOTES_SHARE_RATE_PER_MINUTE", "30")
	t.Setenv("THORNOTES_SHARE_RATE_PER_HOUR", "300")

	cfg, err := Parse()
	require.NoError(t, err)
	assert.Equal(t, 30, cfg.ShareRatePerMinute)
	assert.Equal(t, 300, cfg.ShareRatePerHour)
}

// TestParse_ShareRate_ZeroDisables is the documented opt-out: 0 turns one window
// off rather than refusing every read.
func TestParse_ShareRate_ZeroDisables(t *testing.T) {
	resetFlags()
	os.Unsetenv("THORNOTES_ADDR")
	os.Unsetenv("THORNOTES_TRUSTED_PROXY")
	t.Setenv("THORNOTES_SHARE_RATE_PER_MINUTE", "0")
	t.Setenv("THORNOTES_SHARE_RATE_PER_HOUR", "0")

	cfg, err := Parse()
	require.NoError(t, err)
	assert.Equal(t, 0, cfg.ShareRatePerMinute)
	assert.Equal(t, 0, cfg.ShareRatePerHour)
}

// TestParse_NegativeShareRate_ReturnsError: a negative budget is a typo, not a
// request to disable, and it must fail at startup rather than quietly leave the
// public share surface unbounded.
func TestParse_NegativeShareRate_ReturnsError(t *testing.T) {
	resetFlags()
	os.Unsetenv("THORNOTES_ADDR")
	os.Unsetenv("THORNOTES_TRUSTED_PROXY")
	t.Setenv("THORNOTES_SHARE_RATE_PER_MINUTE", "-1")

	_, err := Parse()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid --share-rate-per-minute")
}
