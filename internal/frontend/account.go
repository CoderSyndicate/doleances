package frontend

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/rs/zerolog/log"
	qrcode "github.com/skip2/go-qrcode"

	"github.com/CoderSyndicate/doleances/internal/apiclient"
)

// maxAccountFormBytes bounds an account form: a name, a device label, a
// choice. Nothing here is long.
const maxAccountFormBytes = 16 << 10

// notificationsPerPage is how many items the list shows at once.
const notificationsPerPage = 25

// signinPage is where somebody makes an account or comes back to one.
//
// One page for both, because with passkeys they are nearly the same act and
// the browser knows which: signing in offers the credentials it already holds,
// and making an account asks for a new one. Two pages would make somebody
// choose between two words for a thing they have no way to distinguish.
type signinPage struct {
	page

	// Next is where to go once signed in, so somebody who pressed "join" on a
	// group arrives back at that group rather than at a dashboard.
	Next string

	// Recovering opens the recovery-code form, for whoever has lost every
	// device.
	Recovering bool
}

func (s *site) signin(w http.ResponseWriter, r *http.Request) {
	data := signinPage{page: s.newPage(r, "account.signin_title")}
	data.Next = safeNext(r.URL.Query().Get("next"))
	data.Recovering = r.URL.Query().Get("recover") != ""
	s.renderer.Render(w, http.StatusOK, "signin", data)
}

// safeNext bounds where a sign-in may send somebody afterwards.
//
// Only a path on this site. A `next` that could name another origin is an open
// redirect, and an open redirect on the page where people sign in is how a
// convincing phishing link gets built out of a real domain.
func safeNext(next string) string {
	if next == "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") {
		return ""
	}
	return next
}

// accountPage is somebody's own account.
type accountPage struct {
	page

	Account apiclient.Account
	Devices apiclient.Devices

	// PushKey is the VAPID public key, handed to the page so its script can
	// subscribe this browser. It is a public key: it authorises nothing and is
	// meant to be published.
	PushKey string

	// PushConfigured says whether this installation can send at all. When it
	// cannot, the page says so instead of offering a permission prompt for
	// something that would never arrive.
	PushConfigured bool

	// Codes are freshly issued recovery codes, shown once. Empty on every
	// other render, because nothing can recover them.
	Codes []string

	// Link is a device-linking offer: the address to open on the new device.
	Link string

	// LinkQR is that address as a QR code, so a phone does not have to be
	// typed at. A data URI rather than a fetched image: the token is in it,
	// and an image URL carrying a credential is an image URL in every cache
	// and access log between here and the screen.
	LinkQR string

	// Waiting is a device asking to be let in, for this account to approve or
	// refuse. Refusing is the case that matters — somebody who did not start a
	// linking sees a browser they do not recognise and says no.
	Waiting apiclient.PendingLink

	Saved bool
	Error string
}

func (s *site) account(w http.ResponseWriter, r *http.Request) {
	if !signedIn(r) {
		s.toSignin(w, r, "/account")
		return
	}
	s.renderAccount(w, r, http.StatusOK, nil)
}

// toSignin sends somebody to sign in and back again.
func (s *site) toSignin(w http.ResponseWriter, r *http.Request, next string) {
	target := "/account/signin"
	if next != "" {
		target += "?next=" + url.QueryEscape(next)
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func (s *site) renderAccount(w http.ResponseWriter, r *http.Request, status int, adjust func(*accountPage)) {
	reader := s.reader(r)

	account, err := reader.Me(r.Context())
	if err != nil {
		// An expired or revoked session lands here. The cookie is cleared so
		// the next page is not stuck in the same state, and the person is
		// asked to sign in rather than shown an error they cannot act on.
		log.Debug().Err(err).Msg("cannot read an account; treating the session as gone")
		s.clearSession(w)
		s.toSignin(w, r, "/account")
		return
	}

	data := accountPage{page: s.newPage(r, "account.title")}
	data.Account = account
	data.PushConfigured = account.PushConfigured

	if devices, err := reader.Devices(r.Context()); err != nil {
		// The rest of the page is still worth serving: somebody who came to
		// change their name should not be stopped by the device list.
		log.Warn().Err(err).Msg("cannot read an account's devices")
	} else {
		data.Devices = devices
	}

	if account.PushConfigured {
		if key, _, err := reader.PushKey(r.Context()); err == nil {
			data.PushKey = key
		}
	}

	if waiting, err := reader.ReadPendingLink(r.Context()); err == nil {
		data.Waiting = waiting
	}

	data.Saved = r.URL.Query().Get("saved") != ""
	if key := r.URL.Query().Get("error"); key != "" {
		data.Error = data.T(accountErrors[key])
	}

	if adjust != nil {
		adjust(&data)
	}
	s.renderer.Render(w, status, "account", data)
}

// accountErrors maps what went wrong to what to say about it.
//
// Named rather than numbered, so the address bar says what happened and an
// unknown value lands on the general sentence rather than an empty box.
var accountErrors = map[string]string{
	"last":    "account.error_last_passkey",
	"name":    "account.error_name",
	"general": "account.error_general",
}

// saveAccount is every button on the account page.
//
// One endpoint, because each is a form post returning to the same page and
// which one was pressed is the field that is filled. The alternative is nine
// routes that differ only in the line that calls the backend.
func (s *site) saveAccount(w http.ResponseWriter, r *http.Request) {
	if !signedIn(r) {
		s.toSignin(w, r, "/account")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxAccountFormBytes)
	if err := r.ParseForm(); err != nil {
		s.renderNotFound(w, r)
		return
	}

	reader := s.reader(r)
	back := func(query string) { http.Redirect(w, r, "/account"+query, http.StatusSeeOther) }

	switch {
	case r.PostFormValue("rename") != "":
		name := strings.TrimSpace(r.PostFormValue("name"))
		if _, err := reader.RenameAccount(r.Context(), name); err != nil {
			log.Debug().Err(err).Msg("cannot rename an account")
			back("?error=name")
			return
		}

	case r.PostFormValue("signout") != "":
		// Signed out here as well as at the backend. The backend deletes the
		// session row; this removes the cookie, and forgetting either leaves a
		// device that looks signed in and cannot do anything, or one that is
		// signed out and still holds a working token.
		if err := reader.SignOut(r.Context()); err != nil {
			log.Warn().Err(err).Msg("cannot sign a device out at the backend")
		}
		s.clearSession(w)
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return

	case r.PostFormValue("delete") != "":
		if err := reader.DeleteAccount(r.Context()); err != nil {
			log.Error().Err(err).Msg("cannot delete an account")
			back("?error=general")
			return
		}
		s.clearSession(w)
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return

	case r.PostFormValue("codes") != "":
		codes, err := reader.ReissueRecoveryCodes(r.Context())
		if err != nil {
			log.Error().Err(err).Msg("cannot reissue recovery codes")
			back("?error=general")
			return
		}
		// Rendered rather than redirected, and this is the one case where that
		// is required: the codes exist in this response and nowhere else, ever.
		// A redirect would either lose them or put them in a query string.
		s.renderAccount(w, r, http.StatusOK, func(data *accountPage) {
			data.Codes = codes
		})
		return

	case r.PostFormValue("rename_passkey") != "":
		id := r.PostFormValue("rename_passkey")
		name := strings.TrimSpace(r.PostFormValue("passkey_name"))
		if _, err := reader.RenamePasskey(r.Context(), id, name); err != nil {
			log.Debug().Err(err).Msg("cannot rename a passkey")
			back("?error=general")
			return
		}

	case r.PostFormValue("remove_passkey") != "":
		_, err := reader.DeletePasskey(r.Context(), r.PostFormValue("remove_passkey"))
		if err != nil {
			log.Debug().Err(err).Msg("cannot remove a passkey")
			// The refusal that matters, and the one somebody can act on: it
			// was their only way in.
			if strings.Contains(err.Error(), "only passkey") {
				back("?error=last")
				return
			}
			back("?error=general")
			return
		}

	case r.PostFormValue("revoke") != "":
		if _, err := reader.RevokeSession(r.Context(), r.PostFormValue("revoke")); err != nil {
			log.Debug().Err(err).Msg("cannot revoke a session")
			back("?error=general")
			return
		}

	case r.PostFormValue("unsubscribe") != "":
		if err := reader.Unsubscribe(r.Context(), r.PostFormValue("unsubscribe")); err != nil {
			log.Debug().Err(err).Msg("cannot unsubscribe a device")
			back("?error=general")
			return
		}

	case r.PostFormValue("approve") != "":
		if err := s.approveLink(r, reader, r.PostFormValue("approve"), true); err != nil {
			log.Debug().Err(err).Msg("cannot approve a device")
			back("?error=general")
			return
		}

	case r.PostFormValue("refuse") != "":
		if err := s.approveLink(r, reader, r.PostFormValue("refuse"), false); err != nil {
			log.Debug().Err(err).Msg("cannot refuse a device")
			back("?error=general")
			return
		}

	case r.PostFormValue("offer") != "":
		// A fresh linking token, rendered rather than redirected for the same
		// reason the recovery codes are: it lives two minutes and it is in this
		// response.
		s.offerLink(w, r, reader)
		return
	}

	back("?saved=1")
}

// offerLink mints a device-linking token and shows it.
//
// The whole exchange is on this page and the one the other device opens:
//
//  1. this device asks for a token and shows it as a QR code;
//  2. the new device opens it and says what it is;
//  3. **this device approves what claimed it**, which is what stops a relayed
//     code — somebody who tricked a person into scanning theirs still has to
//     get that person to approve a browser they do not recognise;
//  4. the new device registers a passkey and is signed in.
//
// Two minutes, single use. It exists because synced passkeys only cross
// devices inside one ecosystem and the browser's own QR flow needs Bluetooth
// proximity: somebody with an Android phone and a Mac has neither, and without
// this they would need a second account and lose their groups with it.
func (s *site) offerLink(w http.ResponseWriter, r *http.Request, reader *apiclient.Client) {
	answer, status, err := reader.Forward(r.Context(), apiclient.Forwarded{
		Method:  http.MethodPost,
		Path:    "/v1/accounts/me/link",
		Body:    []byte("{}"),
		Session: session(r),
		From:    s.clientAddress(r),
	})
	if err != nil || status >= http.StatusBadRequest {
		log.Error().Err(err).Int("status", status).Msg("cannot open a device link")
		http.Redirect(w, r, "/account?error=general", http.StatusSeeOther)
		return
	}

	var offer struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(answer, &offer); err != nil || offer.Token == "" {
		log.Error().Err(err).Msg("a device link carried no token")
		http.Redirect(w, r, "/account?error=general", http.StatusSeeOther)
		return
	}

	target := s.siteURL + "/account/link?t=" + url.QueryEscape(offer.Token)

	s.renderAccount(w, r, http.StatusOK, func(data *accountPage) {
		data.Link = target
		data.LinkQR = qrDataURI(target)
	})
}

// qrDataURI renders an address as a QR code, inline.
//
// A data URI rather than an image this service serves: the token is in the
// address, and an image URL carrying a credential is an image URL written down
// by every cache and access log between here and the screen.
//
// A failure produces no image and no error. The address is shown as text
// beside it and is the substance; the code is the convenience.
func qrDataURI(target string) string {
	// Medium recovery: the code is read off a screen at arm's length rather
	// than off a poster, so the extra redundancy of a higher level buys
	// nothing and makes the pattern denser.
	png, err := qrcode.Encode(target, qrcode.Medium, 320)
	if err != nil {
		log.Warn().Err(err).Msg("cannot render a linking QR code")
		return ""
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
}

// approveLink is this device answering for the one that is asking.
func (s *site) approveLink(r *http.Request, reader *apiclient.Client, id string, approve bool) error {
	method, path := http.MethodDelete, "/v1/accounts/me/link/"+url.PathEscape(id)
	if approve {
		method, path = http.MethodPost, path+"/approve"
	}

	_, status, err := reader.Forward(r.Context(), apiclient.Forwarded{
		Method:  method,
		Path:    path,
		Body:    []byte("{}"),
		Session: session(r),
		From:    s.clientAddress(r),
	})
	if err != nil {
		return err
	}
	if status >= http.StatusBadRequest {
		return fmt.Errorf("the backend answered %d", status)
	}
	return nil
}

// linkPage is what the *new* device sees.
type linkPage struct {
	page

	// Token is carried into the page's script, which claims it, waits for the
	// other device to approve, and then registers a passkey.
	Token string

	// Failed covers expired, already used and never issued alike. Telling them
	// apart would let somebody probe which codes had been real.
	Failed bool
}

func (s *site) linkDevice(w http.ResponseWriter, r *http.Request) {
	data := linkPage{page: s.newPage(r, "account.link_title")}

	value := strings.TrimSpace(r.URL.Query().Get("t"))
	if value == "" {
		data.Failed = true
		s.renderer.Render(w, http.StatusNotFound, "link", data)
		return
	}

	data.Token = value
	s.renderer.Render(w, http.StatusOK, "link", data)
}

// notificationsPage is somebody's own list.
//
// # The list is the channel
//
// A push is a tap on the shoulder and nothing more. An iPhone that has not
// installed this site receives no push at all, a permission can be refused, and
// an endpoint can die between one week and the next — so everything is written
// here first and delivered second, and somebody who never allows a
// notification still finds out by coming back.
type notificationsPage struct {
	page

	List apiclient.Notifications

	// Page and HasMore drive the paging. A list somebody has been away from
	// for a month is a long one.
	Page    int
	HasMore bool
}

func (s *site) notifications(w http.ResponseWriter, r *http.Request) {
	if !signedIn(r) {
		s.toSignin(w, r, "/notifications")
		return
	}

	number := 1
	if parsed, err := strconv.Atoi(r.URL.Query().Get("page")); err == nil && parsed > 1 {
		number = parsed
	}

	list, err := s.reader(r).ListNotifications(r.Context(),
		notificationsPerPage, (number-1)*notificationsPerPage)
	if err != nil {
		log.Debug().Err(err).Msg("cannot read the notifications; treating the session as gone")
		s.clearSession(w)
		s.toSignin(w, r, "/notifications")
		return
	}

	data := notificationsPage{page: s.newPage(r, "notifications.title")}
	data.List = list
	data.Page = number
	data.HasMore = int64(number*notificationsPerPage) < list.Total
	s.renderer.Render(w, http.StatusOK, "notifications", data)
}

// saveNotifications is read, read-all and remove.
func (s *site) saveNotifications(w http.ResponseWriter, r *http.Request) {
	if !signedIn(r) {
		s.toSignin(w, r, "/notifications")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxAccountFormBytes)
	if err := r.ParseForm(); err != nil {
		s.renderNotFound(w, r)
		return
	}

	reader := s.reader(r)
	var err error
	switch {
	case r.PostFormValue("read_all") != "":
		err = reader.ReadAllNotifications(r.Context())
	case r.PostFormValue("read") != "":
		err = reader.ReadNotification(r.Context(), r.PostFormValue("read"))
	case r.PostFormValue("remove") != "":
		err = reader.DeleteNotification(r.Context(), r.PostFormValue("remove"))
	}
	if err != nil {
		log.Debug().Err(err).Msg("cannot change a notification")
	}

	http.Redirect(w, r, "/notifications", http.StatusSeeOther)
}

// myGroupsPage is the groups somebody belongs to.
type myGroupsPage struct {
	page

	Groups []apiclient.MyGroup
}

func (s *site) myGroups(w http.ResponseWriter, r *http.Request) {
	if !signedIn(r) {
		s.toSignin(w, r, "/account/groups")
		return
	}

	groups, err := s.reader(r).MyGroups(r.Context())
	if err != nil {
		log.Debug().Err(err).Msg("cannot read somebody's groups; treating the session as gone")
		s.clearSession(w)
		s.toSignin(w, r, "/account/groups")
		return
	}

	data := myGroupsPage{page: s.newPage(r, "mygroups.title")}
	data.Groups = groups
	s.renderer.Render(w, http.StatusOK, "mygroups", data)
}
