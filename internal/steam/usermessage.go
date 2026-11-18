package steam

import (
	"context"
	"errors"
	"net"
	"net/url"
	"strings"

	"github.com/gjrud/steam-achievement-tracker/internal/usererr"
)

// User-facing sentences for Steam failures. None names an endpoint, a status line, a body, an eresult, a URL or a key.
const (
	userMsgKeyRejected = "Steam rejected the Web API key. Check that the key is correct and still valid, then replace it in Settings."
	userMsgPrivate     = "Steam did not share achievements for this game. The profile's game details may be private."
	userMsgRefused     = "Steam refused this request."
	userMsgBadRequest  = "Steam could not process the request."
	userMsgNoData      = "Steam has no data for this request."
	userMsgRateLimited = "Steam is limiting requests right now. Try again in a few minutes."
	userMsgUnavailable = "Steam is temporarily unavailable. Try again later."
	userMsgEResult     = "Steam could not provide this data right now."
	userMsgUnreachable = "Could not reach Steam. Check your internet connection and try again."
	userMsgCanceled    = "The request was canceled."
	userMsgUnreadable  = "Steam sent a response the app could not read."
	userMsgUnexpected  = "Steam returned an unexpected response."
	userMsgGeneric     = "Something went wrong. See the log file for details."
)

// UserMessageSentences returns every sentence UserMessage can return from its own table (not the app's or the
// usererr messages it passes through). Tests use it to pin stored text to these sentences.
func UserMessageSentences() []string {
	return []string{
		userMsgKeyRejected, userMsgPrivate, userMsgRefused, userMsgBadRequest, userMsgNoData, userMsgRateLimited,
		userMsgUnavailable, userMsgEResult, userMsgUnreachable, userMsgCanceled, userMsgUnreadable, userMsgUnexpected,
		userMsgGeneric,
	}
}

// UserMessage converts err into a short, plain-language sentence that is safe to show in the UI or to store for
// display. Steam failures are mapped by their typed cause (found with errors.As), so the wrapping prefix of the
// caller ("achievement schema: ") is dropped. A user-facing error (usererr: the app's sentences, the input checks, and
// the Steam messages that wrap Steam's text as a cause) returns its own sentence, whatever its cause. Every other error
// gets a sentence from its typed cause, or one generic sentence: its raw text is never shown. The caller logs the raw
// error (usererr.LogText for a user-facing one). UserMessage does not change err. A nil err gives "".
func UserMessage(err error) string {
	if err == nil {
		return ""
	}
	// A user-facing error is checked first: its cause (kept for the log) may be a typed Steam error whose sentence
	// must not replace the one the error was built with.
	if ue := usererr.Find(err); ue != nil {
		return ue.Error()
	}
	var statusErr *HTTPStatusError
	if errors.As(err, &statusErr) {
		return httpStatusMessage(statusErr)
	}
	var eresultErr *EResultError
	if errors.As(err, &eresultErr) {
		return userMsgEResult
	}
	if isUnreadableResponse(err) {
		return userMsgUnreadable
	}
	if errors.Is(err, context.Canceled) {
		return userMsgCanceled
	}
	if isTransportFailure(err) {
		return userMsgUnreachable
	}
	return userMsgGeneric
}

func httpStatusMessage(e *HTTPStatusError) string {
	switch {
	case e.Code == 401 || e.Code == 403:
		return authFailureMessage(e)
	case e.Code == 400:
		return userMsgBadRequest
	case e.Code == 404:
		return userMsgNoData
	case e.Code == 429:
		return userMsgRateLimited
	case e.Code >= 500 && e.Code <= 599:
		return userMsgUnavailable
	default:
		return userMsgUnexpected
	}
}

// authFailureMessage chooses the sentence for a 401 or 403 by endpoint. Steam uses 403 both for a rejected key and
// for data the profile does not share, so the endpoint decides. Endpoints that send no key never mean a key problem.
func authFailureMessage(e *HTTPStatusError) string {
	// Steam's key-check page names the key parameter; that means the key, whatever the endpoint.
	if e.Code == 403 && e.hasBody && strings.Contains(e.body, "verify your") && strings.Contains(e.body, "key=") {
		return userMsgKeyRejected
	}
	switch endpointName(e.Path) {
	case "ISteamUser.ResolveVanityURL", "ISteamUser.GetPlayerSummaries", "IPlayerService.GetOwnedGames":
		return userMsgKeyRejected
	case "IPlayerService.GetUserAchievements", "ISteamUserStats.GetPlayerAchievements":
		if e.Code == 403 {
			return userMsgPrivate
		}
		return userMsgKeyRejected
	case "IPlayerService.GetGameAchievements", "ISteamUserStats.GetSchemaForGame":
		if e.Code == 403 {
			return userMsgRefused
		}
		return userMsgKeyRejected
	case "ISteamUserStats.GetGlobalAchievementPercentagesForApp":
		return userMsgRefused
	}
	if e.Code == 401 {
		return userMsgKeyRejected
	}
	return userMsgRefused
}

// endpointName returns "Interface.Method" for a path such as "/IPlayerService/GetOwnedGames/v0001/", so the
// version segment does not matter. A path without two segments gives "".
func endpointName(path string) string {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) < 2 {
		return ""
	}
	return parts[0] + "." + parts[1]
}

func isUnreadableResponse(err error) bool {
	var badResp *badResponseError
	return errors.As(err, &badResp)
}

func isTransportFailure(err error) bool {
	var bodyErr *bodyReadError
	if errors.As(err, &bodyErr) {
		return true
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr)
}
