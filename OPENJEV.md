# OpenJEV support

This fork adds optional [OpenJEV](https://openjev.sh) support alongside the
existing TypeSafe API. Jev is built by [TypeSafe](https://typesafe.ai); OpenJEV
is a free community gateway to the same Jev model. TypeSafe stays the default.

## What was added

- `typesafe/client.go`:
  - Constants `OpenjevBaseURL` (`https://api.openjev.sh`), `OpenjevModel`
    (`openjev`), `OpenjevAPIKeyEnv` (`OPENJEV_API_KEY`), and `ProviderEnv`
    (`JEV_PROVIDER`); provider constants `ProviderTypeSafe` and
    `ProviderOpenjev`.
  - New `Options` fields: `Provider` (`"typesafe"` / `"openjev"`) and
    `OpenjevAPIKey`.
  - `New()` implements the provider selection rule (see below).
- `typesafe/retry.go`: HTTP 503 is now retried alongside 429 and 529
  (OpenJEV signals temporary unavailability with 503).
- `README.md`: short OpenJEV note after the intro, provider documentation in
  the Usage section, and updated retry description.

No TypeSafe code path was removed, renamed, or re-defaulted.

## Provider selection rule

1. **Explicit choice wins:** `Options.Provider` (or `JEV_PROVIDER` env) set to
   `"openjev"` or `"typesafe"` selects that provider.
2. **Otherwise, TypeSafe if its key is set:** when `TYPESAFE_API_KEY` (or
   `Options.APIKey`) is present, TypeSafe is used exactly as before —
   `jev-latest`, `https://api.typesafe.ai`. This is the default, unchanged.
3. **Otherwise, OpenJEV if only its key is set:** when only `OPENJEV_API_KEY`
   is present, OpenJEV is used automatically — model `openjev`,
   `https://api.openjev.sh`.
4. If neither key is available, `New()` returns an error.

Anyone with a TypeSafe key sees zero behaviour change.

## Configuration

```bash
# TypeSafe (default, unchanged)
export TYPESAFE_API_KEY='...'

# OpenJEV — explicit
export JEV_PROVIDER=openjev
export OPENJEV_API_KEY='...'

# OpenJEV — implicit (no TypeSafe key set)
export OPENJEV_API_KEY='...'
```

Or in Go:

```go
client, err := typesafe.New(&typesafe.Options{
    Provider:      typesafe.ProviderOpenjev,
    OpenjevAPIKey: os.Getenv("OPENJEV_API_KEY"),
})
```

## Verification

- A live `POST https://api.openjev.sh/v1/systemone` request was sent with model
  `openjev`, state `ping`, and one noul question using the OpenJEV API key. It
  returned HTTP 200.
- `grep -r "api.typesafe.ai"` confirms no hardcoded `api.typesafe.ai` default
  remains in the added code; the TypeSafe default URL is untouched for
  backward compatibility.

## Upstream

Original project: https://github.com/craigh33/adk-go-typesafe by @craigh33.