// Package pricing resolves per-model token rates and computes request cost.
// Rates are USD per million tokens. The table is a built-in default; a model
// with no match has no price and yields zero cost rather than a wrong one.
package pricing

import (
	"regexp"
	"strings"
	"sync"
)

// Rate is the USD-per-million-token price of one model. Cached, Reasoning and
// CacheWrite fall back to Input/Output when a provider does not price them
// separately.
type Rate struct {
	Input      float64
	Output     float64
	Cached     float64
	Reasoning  float64
	CacheWrite float64
}

// Tokens is the token breakdown of a single request.
type Tokens struct {
	Prompt     int
	Completion int
	Cached     int
	CacheWrite int
	Reasoning  int
}

// Cost prices a request. Cached and cache-write tokens are billed at their own
// rate and subtracted from the prompt total so no token is charged twice.
// Reasoning tokens are already counted inside Completion by every upstream we
// support, so they are re-priced only by the delta between the two rates.
func (r Rate) Cost(t Tokens) float64 {
	cached, write := t.Cached, t.CacheWrite
	fresh := t.Prompt - cached - write
	if fresh < 0 {
		fresh = 0
	}

	cost := float64(fresh) * r.Input / 1e6
	cost += float64(cached) * r.rate(r.Cached, r.Input) / 1e6
	cost += float64(write) * r.rate(r.CacheWrite, r.Input) / 1e6
	cost += float64(t.Completion) * r.Output / 1e6

	// Reasoning tokens are a subset of completion tokens; charge only the
	// difference when the model prices them above normal output.
	if t.Reasoning > 0 {
		if delta := r.rate(r.Reasoning, r.Output) - r.Output; delta != 0 {
			cost += float64(t.Reasoning) * delta / 1e6
		}
	}
	return cost
}

func (r Rate) rate(specific, fallback float64) float64 {
	if specific > 0 {
		return specific
	}
	return fallback
}

// models prices exact model identifiers.
var models = map[string]Rate{
	"MiniMax-M2.1":               {Input: 0.5, Output: 2.0, Cached: 0.25, Reasoning: 3.0, CacheWrite: 0.5},
	"MiniMax-M2.5":               {Input: 0.5, Output: 2.0, Cached: 0.25, Reasoning: 3.0, CacheWrite: 0.5},
	"MiniMax-M2.7":               {Input: 0.5, Output: 2.0, Cached: 0.25, Reasoning: 3.0, CacheWrite: 0.5},
	"MiniMax-M3":                 {Input: 0.3, Output: 1.2, Cached: 0.06, Reasoning: 1.8, CacheWrite: 0.3},
	"auto":                       {Input: 2.0, Output: 8.0, Cached: 1.0, Reasoning: 12.0, CacheWrite: 2.0},
	"claude-3-5-sonnet-20241022": {Input: 3.0, Output: 15.0, Cached: 1.5, Reasoning: 15.0, CacheWrite: 3.0},
	"claude-fable-5":             {Input: 10.0, Output: 50.0, Cached: 1.0, Reasoning: 50.0, CacheWrite: 12.5},
	"claude-haiku-4-5-20251001":  {Input: 1.0, Output: 5.0, Cached: 0.1, Reasoning: 5.0, CacheWrite: 1.25},
	"claude-haiku-4.5":           {Input: 0.5, Output: 2.5, Cached: 0.05, Reasoning: 3.75, CacheWrite: 0.5},
	"claude-opus-4-20250514":     {Input: 15.0, Output: 25.0, Cached: 7.5, Reasoning: 112.5, CacheWrite: 15.0},
	"claude-opus-4-5-20251101":   {Input: 5.0, Output: 25.0, Cached: 0.5, Reasoning: 25.0, CacheWrite: 6.25},
	"claude-opus-4-5-thinking":   {Input: 5.0, Output: 25.0, Cached: 0.5, Reasoning: 37.5, CacheWrite: 5.0},
	"claude-opus-4-6":            {Input: 5.0, Output: 25.0, Cached: 0.5, Reasoning: 25.0, CacheWrite: 6.25},
	"claude-opus-4-6-thinking":   {Input: 5.0, Output: 25.0, Cached: 0.5, Reasoning: 37.5, CacheWrite: 5.0},
	"claude-opus-4.1":            {Input: 5.0, Output: 25.0, Cached: 0.5, Reasoning: 37.5, CacheWrite: 5.0},
	"claude-opus-4.5":            {Input: 5.0, Output: 25.0, Cached: 0.5, Reasoning: 37.5, CacheWrite: 5.0},
	"claude-opus-4.6":            {Input: 5.0, Output: 25.0, Cached: 0.5, Reasoning: 37.5, CacheWrite: 5.0},
	"claude-sonnet-4":            {Input: 3.0, Output: 15.0, Cached: 0.3, Reasoning: 22.5, CacheWrite: 3.0},
	"claude-sonnet-4-20250514":   {Input: 3.0, Output: 15.0, Cached: 1.5, Reasoning: 15.0, CacheWrite: 3.0},
	"claude-sonnet-4-5-20250929": {Input: 3.0, Output: 15.0, Cached: 0.3, Reasoning: 15.0, CacheWrite: 3.75},
	"claude-sonnet-4-6":          {Input: 3.0, Output: 15.0, Cached: 0.3, Reasoning: 15.0, CacheWrite: 3.75},
	"claude-sonnet-4.5":          {Input: 3.0, Output: 15.0, Cached: 0.3, Reasoning: 22.5, CacheWrite: 3.0},
	"claude-sonnet-4.6":          {Input: 3.0, Output: 15.0, Cached: 0.3, Reasoning: 22.5, CacheWrite: 3.0},
	"coder-model":                {Input: 1.5, Output: 6.0, Cached: 0.75, Reasoning: 9.0, CacheWrite: 1.5},
	"deepseek-chat":              {Input: 0.14, Output: 0.28, Cached: 0.0028, Reasoning: 0.28, CacheWrite: 0.14},
	"deepseek-r1":                {Input: 0.14, Output: 0.28, Cached: 0.0028, Reasoning: 0.28, CacheWrite: 0.14},
	"deepseek-reasoner":          {Input: 0.14, Output: 0.28, Cached: 0.0028, Reasoning: 0.28, CacheWrite: 0.14},
	"deepseek-v3.2-chat":         {Input: 0.14, Output: 0.28, Cached: 0.0028, Reasoning: 0.28, CacheWrite: 0.14},
	"deepseek-v3.2-reasoner":     {Input: 0.14, Output: 0.28, Cached: 0.0028, Reasoning: 0.28, CacheWrite: 0.14},
	"deepseek-v4-flash":          {Input: 0.14, Output: 0.28, Cached: 0.0028, Reasoning: 0.28, CacheWrite: 0.14},
	"deepseek-v4-pro":            {Input: 0.435, Output: 0.87, Cached: 0.003625, Reasoning: 0.87, CacheWrite: 0.435},
	"gemini-2.5-flash":           {Input: 0.3, Output: 2.5, Cached: 0.03, Reasoning: 3.75, CacheWrite: 0.3},
	"gemini-2.5-flash-lite":      {Input: 0.15, Output: 1.25, Cached: 0.015, Reasoning: 1.875, CacheWrite: 0.15},
	"gemini-2.5-pro":             {Input: 2.0, Output: 12.0, Cached: 0.25, Reasoning: 18.0, CacheWrite: 2.0},
	"gemini-3-flash":             {Input: 0.5, Output: 3.0, Cached: 0.03, Reasoning: 4.5, CacheWrite: 0.5},
	"gemini-3-flash-agent":       {Input: 0.5, Output: 3.0, Cached: 0.03, Reasoning: 4.5, CacheWrite: 0.5},
	"gemini-3-flash-preview":     {Input: 0.5, Output: 3.0, Cached: 0.03, Reasoning: 4.5, CacheWrite: 0.5},
	"gemini-3-pro-preview":       {Input: 2.0, Output: 12.0, Cached: 0.25, Reasoning: 18.0, CacheWrite: 2.0},
	"gemini-3.1-pro-high":        {Input: 4.0, Output: 18.0, Cached: 0.5, Reasoning: 27.0, CacheWrite: 4.0},
	"gemini-3.1-pro-low":         {Input: 2.0, Output: 12.0, Cached: 0.25, Reasoning: 18.0, CacheWrite: 2.0},
	"gemini-3.5-flash-extra-low": {Input: 0.5, Output: 3.0, Cached: 0.03, Reasoning: 4.5, CacheWrite: 0.5},
	"gemini-3.5-flash-high":      {Input: 0.5, Output: 3.0, Cached: 0.03, Reasoning: 4.5, CacheWrite: 0.5},
	"gemini-3.5-flash-lite":      {Input: 0.3, Output: 2.5, Cached: 0.03, Reasoning: 3.75, CacheWrite: 0.375},
	"gemini-3.5-flash-low":       {Input: 0.5, Output: 3.0, Cached: 0.03, Reasoning: 4.5, CacheWrite: 0.5},
	"gemini-3.6-flash":           {Input: 1.5, Output: 7.5, Cached: 0.15, Reasoning: 11.25, CacheWrite: 1.875},
	"gemini-3.6-flash-high":      {Input: 1.5, Output: 7.5, Cached: 0.15, Reasoning: 11.25, CacheWrite: 1.875},
	"gemini-3.6-flash-low":       {Input: 1.5, Output: 7.5, Cached: 0.15, Reasoning: 11.25, CacheWrite: 1.875},
	"gemini-3.6-flash-medium":    {Input: 1.5, Output: 7.5, Cached: 0.15, Reasoning: 11.25, CacheWrite: 1.875},
	"gemini-3.7-flash":           {Input: 1.5, Output: 7.5, Cached: 0.15, Reasoning: 11.25, CacheWrite: 1.875},
	"gemini-3.7-flash-high":      {Input: 1.5, Output: 7.5, Cached: 0.15, Reasoning: 11.25, CacheWrite: 1.875},
	"gemini-3.7-flash-low":       {Input: 1.5, Output: 7.5, Cached: 0.15, Reasoning: 11.25, CacheWrite: 1.875},
	"gemini-3.7-flash-medium":    {Input: 1.5, Output: 7.5, Cached: 0.15, Reasoning: 11.25, CacheWrite: 1.875},
	"gemini-pro-agent":           {Input: 4.0, Output: 18.0, Cached: 0.5, Reasoning: 27.0, CacheWrite: 4.0},
	"glm-4.6":                    {Input: 0.5, Output: 2.0, Cached: 0.25, Reasoning: 3.0, CacheWrite: 0.5},
	"glm-4.6v":                   {Input: 0.75, Output: 3.0, Cached: 0.375, Reasoning: 4.5, CacheWrite: 0.75},
	"glm-4.7":                    {Input: 0.75, Output: 3.0, Cached: 0.375, Reasoning: 4.5, CacheWrite: 0.75},
	"glm-5":                      {Input: 1.0, Output: 4.0, Cached: 0.5, Reasoning: 6.0, CacheWrite: 1.0},
	"gpt-3.5-turbo":              {Input: 0.5, Output: 1.5, Cached: 0.25, Reasoning: 2.25, CacheWrite: 0.5},
	"gpt-4":                      {Input: 2.5, Output: 10.0, Cached: 1.25, Reasoning: 15.0, CacheWrite: 2.5},
	"gpt-4-turbo":                {Input: 10.0, Output: 30.0, Cached: 5.0, Reasoning: 45.0, CacheWrite: 10.0},
	"gpt-4.1":                    {Input: 2.5, Output: 10.0, Cached: 1.25, Reasoning: 15.0, CacheWrite: 2.5},
	"gpt-4o":                     {Input: 2.5, Output: 10.0, Cached: 1.25, Reasoning: 15.0, CacheWrite: 2.5},
	"gpt-4o-mini":                {Input: 0.15, Output: 0.6, Cached: 0.075, Reasoning: 0.9, CacheWrite: 0.15},
	"gpt-5":                      {Input: 1.25, Output: 10.0, Cached: 0.625, Reasoning: 10.0, CacheWrite: 1.25},
	"gpt-5-codex":                {Input: 1.25, Output: 10.0, Cached: 0.625, Reasoning: 10.0, CacheWrite: 1.25},
	"gpt-5-mini":                 {Input: 0.25, Output: 2.0, Cached: 0.125, Reasoning: 2.0, CacheWrite: 0.25},
	"gpt-5.1":                    {Input: 1.25, Output: 10.0, Cached: 0.625, Reasoning: 10.0, CacheWrite: 1.25},
	"gpt-5.1-codex":              {Input: 1.25, Output: 10.0, Cached: 0.625, Reasoning: 10.0, CacheWrite: 1.25},
	"gpt-5.1-codex-max":          {Input: 8.0, Output: 32.0, Cached: 4.0, Reasoning: 48.0, CacheWrite: 8.0},
	"gpt-5.1-codex-mini":         {Input: 1.5, Output: 6.0, Cached: 0.75, Reasoning: 9.0, CacheWrite: 1.5},
	"gpt-5.1-codex-mini-high":    {Input: 2.0, Output: 8.0, Cached: 1.0, Reasoning: 12.0, CacheWrite: 2.0},
	"gpt-5.2":                    {Input: 1.75, Output: 14.0, Cached: 0.175, Reasoning: 14.0, CacheWrite: 1.75},
	"gpt-5.2-codex":              {Input: 1.75, Output: 14.0, Cached: 0.175, Reasoning: 14.0, CacheWrite: 1.75},
	"gpt-5.3-codex":              {Input: 1.75, Output: 14.0, Cached: 0.175, Reasoning: 14.0, CacheWrite: 1.75},
	"gpt-5.3-codex-spark":        {Input: 3.0, Output: 12.0, Cached: 0.3, Reasoning: 12.0, CacheWrite: 3.0},
	"gpt-5.6":                    {Input: 2.5, Output: 15.0, Cached: 0.25, Reasoning: 15.0, CacheWrite: 2.5},
	"gpt-5.6-luna":               {Input: 1.0, Output: 6.0, Cached: 0.1, Reasoning: 6.0, CacheWrite: 1.0},
	"gpt-5.6-sol":                {Input: 5.0, Output: 30.0, Cached: 0.5, Reasoning: 30.0, CacheWrite: 5.0},
	"gpt-5.6-terra":              {Input: 2.5, Output: 15.0, Cached: 0.25, Reasoning: 15.0, CacheWrite: 2.5},
	"gpt-6-astra":                {Input: 10.0, Output: 50.0, Cached: 1.0, Reasoning: 50.0, CacheWrite: 12.5},
	"gpt-oss-120b-medium":        {Input: 0.5, Output: 2.0, Cached: 0.25, Reasoning: 3.0, CacheWrite: 0.5},
	"grok-code-fast-1":           {Input: 0.5, Output: 2.0, Cached: 0.25, Reasoning: 3.0, CacheWrite: 0.5},
	"k3":                         {Input: 3.0, Output: 15.0, Cached: 0.3, Reasoning: 15.0, CacheWrite: 3.0},
	"kimi-for-coding":            {Input: 0.95, Output: 4.0, Cached: 0.19, Reasoning: 4.0, CacheWrite: 0.95},
	"kimi-for-coding-highspeed":  {Input: 1.9, Output: 8.0, Cached: 0.38, Reasoning: 8.0, CacheWrite: 1.9},
	"kimi-k2":                    {Input: 1.0, Output: 4.0, Cached: 0.5, Reasoning: 6.0, CacheWrite: 1.0},
	"kimi-k2-thinking":           {Input: 1.5, Output: 6.0, Cached: 0.75, Reasoning: 9.0, CacheWrite: 1.5},
	"kimi-k2.5":                  {Input: 1.2, Output: 4.8, Cached: 0.6, Reasoning: 7.2, CacheWrite: 1.2},
	"kimi-k2.5-thinking":         {Input: 1.8, Output: 7.2, Cached: 0.9, Reasoning: 10.8, CacheWrite: 1.8},
	"kimi-k2.6":                  {Input: 1.0, Output: 4.0, Cached: 0.5, Reasoning: 6.0, CacheWrite: 1.0},
	"kimi-k2.7-code":             {Input: 0.95, Output: 4.0, Cached: 0.19, Reasoning: 4.0, CacheWrite: 0.95},
	"kimi-k2.7-code-highspeed":   {Input: 1.9, Output: 8.0, Cached: 0.38, Reasoning: 8.0, CacheWrite: 1.9},
	"kimi-k3":                    {Input: 3.0, Output: 15.0, Cached: 0.3, Reasoning: 15.0, CacheWrite: 3.0},
	"kimi-latest":                {Input: 1.0, Output: 4.0, Cached: 0.5, Reasoning: 6.0, CacheWrite: 1.0},
	"minimax-m2.1":               {Input: 0.5, Output: 2.0, Cached: 0.25, Reasoning: 3.0, CacheWrite: 0.5},
	"minimax-m2.5":               {Input: 0.6, Output: 2.4, Cached: 0.3, Reasoning: 3.6, CacheWrite: 0.6},
	"o1":                         {Input: 15.0, Output: 60.0, Cached: 7.5, Reasoning: 90.0, CacheWrite: 15.0},
	"o1-mini":                    {Input: 3.0, Output: 12.0, Cached: 1.5, Reasoning: 18.0, CacheWrite: 3.0},
	"oswe-vscode-prime":          {Input: 1.0, Output: 4.0, Cached: 0.5, Reasoning: 6.0, CacheWrite: 1.0},
	"qwen3-coder-flash":          {Input: 0.5, Output: 2.0, Cached: 0.25, Reasoning: 3.0, CacheWrite: 0.5},
	"qwen3-coder-plus":           {Input: 1.0, Output: 4.0, Cached: 0.5, Reasoning: 6.0, CacheWrite: 1.0},
	"text-embedding-3-large":     {Input: 0.13},
	"text-embedding-3-small":     {Input: 0.02},
	"text-embedding-ada-002":     {Input: 0.1},
	"vision-model":               {Input: 1.5, Output: 6.0, Cached: 0.75, Reasoning: 9.0, CacheWrite: 1.5},
}

// patterns price model families, tried in order after an exact match fails.
var patterns = []struct {
	glob string
	rate Rate
}{
	{"*-codex-xhigh", Rate{Input: 10.0, Output: 40.0, Cached: 5.0, Reasoning: 60.0, CacheWrite: 10.0}},
	{"*-codex-high", Rate{Input: 8.0, Output: 32.0, Cached: 4.0, Reasoning: 48.0, CacheWrite: 8.0}},
	{"*-codex-max", Rate{Input: 8.0, Output: 32.0, Cached: 4.0, Reasoning: 48.0, CacheWrite: 8.0}},
	{"*-codex-mini-*", Rate{Input: 1.5, Output: 6.0, Cached: 0.75, Reasoning: 9.0, CacheWrite: 1.5}},
	{"*-codex-mini", Rate{Input: 1.5, Output: 6.0, Cached: 0.75, Reasoning: 9.0, CacheWrite: 1.5}},
	{"*-codex-low", Rate{Input: 1.75, Output: 14.0, Cached: 0.175, Reasoning: 14.0, CacheWrite: 1.75}},
	{"*-codex-none", Rate{Input: 1.75, Output: 14.0, Cached: 0.175, Reasoning: 14.0, CacheWrite: 1.75}},
	{"*-codex-spark", Rate{Input: 3.0, Output: 12.0, Cached: 0.3, Reasoning: 12.0, CacheWrite: 3.0}},
	{"codex-*", Rate{Input: 1.75, Output: 14.0, Cached: 0.175, Reasoning: 14.0, CacheWrite: 1.75}},
	{"*-codex", Rate{Input: 1.75, Output: 14.0, Cached: 0.175, Reasoning: 14.0, CacheWrite: 1.75}},
	{"claude-opus-*", Rate{Input: 5.0, Output: 25.0, Cached: 0.5, Reasoning: 25.0, CacheWrite: 6.25}},
	{"claude-sonnet-*", Rate{Input: 3.0, Output: 15.0, Cached: 0.3, Reasoning: 15.0, CacheWrite: 3.75}},
	{"claude-haiku-*", Rate{Input: 1.0, Output: 5.0, Cached: 0.1, Reasoning: 5.0, CacheWrite: 1.25}},
	{"claude-*", Rate{Input: 3.0, Output: 15.0, Cached: 0.3, Reasoning: 15.0, CacheWrite: 3.75}},
	{"gemini-*-flash-lite", Rate{Input: 0.15, Output: 1.25, Cached: 0.015, Reasoning: 1.875, CacheWrite: 0.15}},
	{"gemini-*-flash", Rate{Input: 0.3, Output: 2.5, Cached: 0.03, Reasoning: 3.75, CacheWrite: 0.3}},
	{"gemini-*-pro", Rate{Input: 2.0, Output: 12.0, Cached: 0.25, Reasoning: 18.0, CacheWrite: 2.0}},
	{"gemini-3-*", Rate{Input: 0.5, Output: 3.0, Cached: 0.03, Reasoning: 4.5, CacheWrite: 0.5}},
	{"gemini-2.5-*", Rate{Input: 0.3, Output: 2.5, Cached: 0.03, Reasoning: 3.75, CacheWrite: 0.3}},
	{"gemini-*", Rate{Input: 0.5, Output: 3.0, Cached: 0.03, Reasoning: 4.5, CacheWrite: 0.5}},
	{"gpt-6-*", Rate{Input: 10.0, Output: 50.0, Cached: 1.0, Reasoning: 50.0, CacheWrite: 12.5}},
	{"gpt-5.6-*", Rate{Input: 2.5, Output: 15.0, Cached: 0.25, Reasoning: 15.0, CacheWrite: 2.5}},
	{"gpt-5.3-*", Rate{Input: 1.75, Output: 14.0, Cached: 0.175, Reasoning: 14.0, CacheWrite: 1.75}},
	{"gpt-5.2-*", Rate{Input: 1.75, Output: 14.0, Cached: 0.175, Reasoning: 14.0, CacheWrite: 1.75}},
	{"gpt-5.1-*", Rate{Input: 1.25, Output: 10.0, Cached: 0.625, Reasoning: 10.0, CacheWrite: 1.25}},
	{"gpt-5-*", Rate{Input: 1.25, Output: 10.0, Cached: 0.625, Reasoning: 10.0, CacheWrite: 1.25}},
	{"gpt-5*", Rate{Input: 1.25, Output: 10.0, Cached: 0.625, Reasoning: 10.0, CacheWrite: 1.25}},
	{"gpt-4o-*", Rate{Input: 0.15, Output: 0.6, Cached: 0.075, Reasoning: 0.9, CacheWrite: 0.15}},
	{"gpt-4o", Rate{Input: 2.5, Output: 10.0, Cached: 1.25, Reasoning: 15.0, CacheWrite: 2.5}},
	{"gpt-4*", Rate{Input: 2.5, Output: 10.0, Cached: 1.25, Reasoning: 15.0, CacheWrite: 2.5}},
	{"o1-*", Rate{Input: 3.0, Output: 12.0, Cached: 1.5, Reasoning: 18.0, CacheWrite: 3.0}},
	{"o1", Rate{Input: 15.0, Output: 60.0, Cached: 7.5, Reasoning: 90.0, CacheWrite: 15.0}},
	{"o3-*", Rate{Input: 10.0, Output: 40.0, Cached: 5.0, Reasoning: 60.0, CacheWrite: 10.0}},
	{"o4-*", Rate{Input: 2.0, Output: 8.0, Cached: 1.0, Reasoning: 12.0, CacheWrite: 2.0}},
	{"qwen3-coder-*", Rate{Input: 1.0, Output: 4.0, Cached: 0.5, Reasoning: 6.0, CacheWrite: 1.0}},
	{"qwen*-coder-*", Rate{Input: 1.0, Output: 4.0, Cached: 0.5, Reasoning: 6.0, CacheWrite: 1.0}},
	{"qwen*", Rate{Input: 0.5, Output: 2.0, Cached: 0.25, Reasoning: 3.0, CacheWrite: 0.5}},
	{"kimi-*-thinking", Rate{Input: 1.8, Output: 7.2, Cached: 0.9, Reasoning: 10.8, CacheWrite: 1.8}},
	{"kimi-k3*", Rate{Input: 3.0, Output: 15.0, Cached: 0.3, Reasoning: 15.0, CacheWrite: 3.0}},
	{"kimi-k2*", Rate{Input: 1.2, Output: 4.8, Cached: 0.6, Reasoning: 7.2, CacheWrite: 1.2}},
	{"kimi-*", Rate{Input: 1.0, Output: 4.0, Cached: 0.5, Reasoning: 6.0, CacheWrite: 1.0}},
	{"deepseek-*reasoner*", Rate{Input: 0.14, Output: 0.28, Cached: 0.0028, Reasoning: 0.28, CacheWrite: 0.14}},
	{"deepseek-r*", Rate{Input: 0.14, Output: 0.28, Cached: 0.0028, Reasoning: 0.28, CacheWrite: 0.14}},
	{"deepseek-v*", Rate{Input: 0.14, Output: 0.28, Cached: 0.0028, Reasoning: 0.28, CacheWrite: 0.14}},
	{"deepseek-*", Rate{Input: 0.14, Output: 0.28, Cached: 0.0028, Reasoning: 0.28, CacheWrite: 0.14}},
	{"glm-5*", Rate{Input: 1.0, Output: 4.0, Cached: 0.5, Reasoning: 6.0, CacheWrite: 1.0}},
	{"glm-4*", Rate{Input: 0.75, Output: 3.0, Cached: 0.375, Reasoning: 4.5, CacheWrite: 0.75}},
	{"glm-*", Rate{Input: 0.5, Output: 2.0, Cached: 0.25, Reasoning: 3.0, CacheWrite: 0.5}},
	{"MiniMax-*", Rate{Input: 0.5, Output: 2.0, Cached: 0.25, Reasoning: 3.0, CacheWrite: 0.5}},
	{"minimax-*", Rate{Input: 0.5, Output: 2.0, Cached: 0.25, Reasoning: 3.0, CacheWrite: 0.5}},
	{"grok-code-*", Rate{Input: 0.5, Output: 2.0, Cached: 0.25, Reasoning: 3.0, CacheWrite: 0.5}},
	{"grok-*", Rate{Input: 0.5, Output: 2.0, Cached: 0.25, Reasoning: 3.0, CacheWrite: 0.5}},
	{"text-embedding-*", Rate{Input: 0.02}},
}

// globRE compiles a "*" glob into an anchored, case-insensitive regexp.
var globCache sync.Map

func globRE(glob string) *regexp.Regexp {
	if cached, ok := globCache.Load(glob); ok {
		return cached.(*regexp.Regexp)
	}
	parts := strings.Split(glob, "*")
	for i, p := range parts {
		parts[i] = regexp.QuoteMeta(p)
	}
	re := regexp.MustCompile("(?i)^" + strings.Join(parts, ".*") + "$")
	globCache.Store(glob, re)
	return re
}

// For resolves the rate of a model identifier. It tries the full name, then
// the segment after the last "/" so vendor-prefixed aliases like
// "wz/gemini-3.8-flash" match the same entry as "gemini-3.8-flash", then the
// family patterns. The second return reports whether a price was found.
func For(model string) (Rate, bool) {
	if model == "" {
		return Rate{}, false
	}

	candidates := []string{model}
	if _, short, found := strings.Cut(model, "/"); found && short != "" {
		if i := strings.LastIndex(model, "/"); i >= 0 {
			candidates = append(candidates, model[i+1:])
		}
	}

	for _, name := range candidates {
		if r, ok := models[name]; ok {
			return r, true
		}
	}
	for _, p := range patterns {
		for _, name := range candidates {
			if globRE(p.glob).MatchString(name) {
				return p.rate, true
			}
		}
	}
	return Rate{}, false
}

// Cost prices a request for a model, returning 0 when the model has no rate.
func Cost(model string, t Tokens) float64 {
	r, ok := For(model)
	if !ok {
		return 0
	}
	return r.Cost(t)
}
