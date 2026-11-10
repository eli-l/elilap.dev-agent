package postiz

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/eli-l/elilap.dev-agent/internal/config"
	"github.com/eli-l/elilap.dev-agent/internal/logging"
	"github.com/eli-l/elilap.dev-agent/internal/sources/shared"
)

// Postiz post states, as observed in the public API. Scheduled posts that
// have not gone out yet are QUEUE; a post that failed to publish stays ERROR
// (its `error` field was null in the capture, so the state is the signal).
const (
	statePublished = "PUBLISHED"
	stateQueue     = "QUEUE"
)

// titleMaxRunes caps the derived title: Postiz has no title field, so the
// first line of the body doubles as one for chunk titles and FTS.
const titleMaxRunes = 120

// Provider implements shared.Provider over a Postiz instance.
//
// The public API lists every post in a date window (with its full content)
// and has no read-by-id endpoint, so List caches what it fetched and Fetch
// serves that cache. A provider instance is built per ingest run, and ingest
// always lists before fetching, so the cache never outlives its listing.
type Provider struct {
	client *Client
	opts   *Options
	now    func() time.Time

	mu    sync.Mutex
	cache map[string]Post
}

// NewProvider constructs the Postiz provider from parsed options and
// resolved credentials. The API key must already be resolved from the
// environment by the caller (the config layer never prints values).
func NewProvider(opts *Options, apiURL, apiKey string, hc *http.Client) (*Provider, error) {
	client, err := NewClient(apiURL, apiKey, hc)
	if err != nil {
		return nil, err
	}
	return &Provider{
		client: client,
		opts:   opts,
		now:    func() time.Time { return time.Now() },
		cache:  make(map[string]Post),
	}, nil
}

// NewFromSource is the registry constructor: it resolves the source's
// credential env references and builds a Provider from parsed options.
func NewFromSource(src config.Source, opts any, bc shared.BuildContext) (shared.Provider, error) {
	o, ok := opts.(*Options)
	if !ok {
		return nil, fmt.Errorf("postiz: options are %T, want *postiz.Options", opts)
	}
	apiURL, apiKey, err := resolveCredentials(src)
	if err != nil {
		return nil, err
	}
	return NewProvider(o, apiURL, apiKey, bc.Client())
}

// resolveCredentials reads the env var names referenced by the source's
// credentials block. Missing variables produce a clear error naming the
// variable - never the value.
func resolveCredentials(src config.Source) (apiURL, apiKey string, err error) {
	apiURL, err = shared.ResolveEnvRef(src.Credentials.APIBaseURL)
	if err != nil {
		return "", "", err
	}
	apiKey, err = shared.ResolveEnvRef(src.Credentials.APIKey)
	if err != nil {
		return "", "", err
	}
	return apiURL, apiKey, nil
}

// Type reports the config type string.
func (p *Provider) Type() string { return "postiz" }

// List returns the cross-posts in [since, now] that the options select:
// configured platforms only, published posts (plus past-scheduled ones when
// include_scheduled is set), never drafts or failures.
//
// The public API bounds a listing by publishDate and returns no modification
// time, so `since` is compared against publishDate and an unbounded listing
// starts at the Unix epoch.
func (p *Provider) List(ctx context.Context, since time.Time) ([]shared.Item, error) {
	log := logging.Sources().With().Str("provider", p.Type()).Logger()

	now := p.now().UTC()
	start := since.UTC()
	if start.IsZero() {
		start = time.Unix(0, 0).UTC()
	}
	if start.After(now) {
		log.Warn().
			Time("since", start).Time("now", now).
			Msg("listing window starts after now: nothing to list")
		return nil, nil
	}

	posts, err := p.client.ListPosts(ctx, start, now)
	if err != nil {
		return nil, err
	}

	wanted := p.opts.platformSet()
	var (
		items       []shared.Item
		seen        = make(map[string]bool, len(posts))
		skipped     map[string]int
		perPlatform = make(map[string]int, len(wanted))
	)
	for _, post := range posts {
		platform := platformOf(post)
		if !wanted[platform] {
			continue
		}
		perPlatform[platform]++
		if seen[post.ID] {
			continue // a repeated page: the API has no pagination to cause it
		}
		if reason := p.skipReason(post, now); reason != "" {
			if skipped == nil {
				skipped = make(map[string]int, 4)
			}
			skipped[reason]++
			continue
		}
		seen[post.ID] = true

		p.mu.Lock()
		p.cache[post.ID] = post
		p.mu.Unlock()

		items = append(items, shared.Item{
			ID:           post.ID,
			Ref:          "", // no read-by-id endpoint; Fetch uses the cache
			URL:          post.ReleaseURL,
			ETag:         fingerprint(post),
			LastModified: post.PublishDate,
			Hint:         fmt.Sprintf("state=%s platform=%s", post.State, platform),
		})
	}

	if len(perPlatform) > 0 {
		log.Debug().Interface("platforms", perPlatform).Msg("listed posts per platform")
	}
	// Only an unbounded listing can prove a platform is unused: an
	// incremental window legitimately misses a network that had no post that
	// day, and warning there would be routine noise.
	missing := p.missingPlatforms(perPlatform)
	if len(missing) > 0 && since.IsZero() {
		log.Warn().
			Strs("configured", p.opts.Platforms).
			Strs("missing", missing).
			Msg("configured platforms returned no posts in the window (check the Postiz provider identifiers)")
	}
	if len(skipped) > 0 {
		log.Debug().Interface("skipped_states", skipped).Msg("skipped posts by state")
	}
	if skipped["unknown_state"] > 0 {
		log.Warn().
			Int("posts", skipped["unknown_state"]).
			Msg("skipped posts in an unrecognized state (upstream added a state?); only PUBLISHED and QUEUE are imported")
	}
	log.Info().
		Time("since", since).
		Int("posts", len(posts)).
		Int("items", len(items)).
		Msg("listed cross-posts")
	return items, nil
}

// Fetch returns the normalized content of one post previously returned by
// List. The public API has no read-by-id endpoint, so this serves the post
// captured during the listing.
func (p *Provider) Fetch(ctx context.Context, it shared.Item) (shared.Document, error) {
	p.mu.Lock()
	post, ok := p.cache[it.ID]
	p.mu.Unlock()
	if !ok {
		return shared.Document{}, fmt.Errorf(
			"postiz: item %s was not in the last listing (the public API exposes no per-post endpoint); re-run ingest to list it",
			it.ID)
	}

	platform := platformOf(post)
	author := strings.TrimSpace(post.Integration.Name)
	if author == "" {
		author = platform
	}
	doc := shared.Document{
		Title:       titleOf(post.Content),
		Body:        post.Content,
		PublishedAt: post.PublishDate,
		// The API exposes no modified time. Publish time is the honest
		// stand-in; content edits are caught by the ledger ETag instead.
		UpdatedAt: post.PublishDate,
		Author:    author,
		Tags:      post.Tags,
		Platform:  platform,
	}
	doc.Meta = map[string]any{
		"postiz_id":       post.ID,
		"state":           post.State,
		"group":           post.Group,
		"release_url":     post.ReleaseURL,
		"release_id":      post.ReleaseID,
		"integration_id":  post.Integration.ID,
		"integration":     post.Integration.Name,
		"creation_method": post.CreationMethod,
		"publish_date":    post.PublishDate.UTC().Format(time.RFC3339),
	}
	if post.ReleaseURL != "" {
		doc.Meta["url"] = post.ReleaseURL
	}

	log := logging.Sources().With().Str("provider", p.Type()).Str("item_id", it.ID).Logger()
	log.Debug().
		Str("platform", platform).
		Str("state", post.State).
		Int("chars", len(post.Content)).
		Msg("fetched cross-post")
	return doc, nil
}

// skipReason reports why a post must not be indexed, or "" when it should be.
// Blocks, by PLAN.md: never drafts, never failures; scheduled (QUEUE) posts
// only once their publish time has passed and only when include_scheduled is
// set. An unrecognized state is skipped rather than guessed at.
func (p *Provider) skipReason(post Post, now time.Time) string {
	switch strings.ToUpper(strings.TrimSpace(post.State)) {
	case statePublished:
		return ""
	case stateQueue:
		if !p.opts.IncludeScheduled {
			return "scheduled"
		}
		if post.PublishDate.After(now) {
			return "scheduled_future"
		}
		return ""
	case "DRAFT":
		return "draft"
	case "ERROR":
		return "failed"
	default:
		return "unknown_state"
	}
}

// missingPlatforms returns the configured platforms that produced no posts,
// in configuration order, so a provider-identifier mismatch is visible in the
// log instead of looking like an empty upstream.
func (p *Provider) missingPlatforms(counts map[string]int) []string {
	var missing []string
	for _, platform := range p.opts.Platforms {
		if counts[platform] == 0 {
			missing = append(missing, platform)
		}
	}
	return missing
}

// platformOf returns the normalized platform identifier of a post, "" when
// the post carries no integration.
func platformOf(post Post) string {
	return strings.ToLower(strings.TrimSpace(post.Integration.ProviderIdentifier))
}

// fingerprint hashes the cheap fields that decide whether a listed post still
// matches the ledger. Postiz has no ETag and no modified time, so the content
// and state are folded into one; a QUEUE post that got published changes its
// ETag even though its publishDate does not move.
func fingerprint(post Post) string {
	h := sha256.Sum256([]byte(strings.Join([]string{
		shared.ContentHash(post.Content),
		post.State,
		post.PublishDate.UTC().Format(time.RFC3339),
		platformOf(post),
	}, "\x00")))
	return "sha256:" + hex.EncodeToString(h[:16])
}

// titleOf derives a document title from the first line of a post body.
// Postiz has no title field, and an empty title would leave every cross-post
// chunk untitled in FTS; the first line is the honest stand-in.
func titleOf(content string) string {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		return truncateRunes(line, titleMaxRunes)
	}
	return ""
}

// truncateRunes cuts s to at most n runes, preferring the last word boundary
// so a title does not end mid-word. Multi-byte text is never split.
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	cut := string(r[:n])
	// LastIndexAny returns a byte offset, so compare it with the byte length
	// of the cut, not the rune count: for multi-byte text the two differ and
	// the wrong unit trims far earlier than the halfway point.
	if i := strings.LastIndexAny(cut, " \t"); i > len(cut)/2 {
		cut = cut[:i]
	}
	return strings.TrimRight(cut, " \t.,;:-")
}
