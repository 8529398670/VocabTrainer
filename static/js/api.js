// Every call to the server goes through here, so the things that are easy to
// forget -- sending the cookie, attaching the CSRF token, turning a non-2xx
// response into a real Error -- happen once instead of at each call site.
const Api = {
  csrfToken: null,

  async request( path , options ) {
    const settings = options || {};
    const response = await fetch( path , {
      method: settings.method || "GET",
      // The session cookie is the only credential; without this an
      // authenticated request silently arrives as an anonymous one.
      credentials: "same-origin",
      headers: { "Content-Type": "application/json" },
      body: settings.body ? JSON.stringify( settings.body ) : undefined,
    } );

    let payload = {};
    try {
      payload = await response.json();
    } catch ( parseError ) {
      payload = {};
    }

    if ( !response.ok ) {
      const error = new Error( payload.error || response.statusText );
      error.status = response.status;
      // Some endpoints add a short machine-readable code next to the
      // message -- an invite that is full rather than merely wrong, say --
      // so the caller can look the wording up in language.yaml instead of
      // showing prose the server invented. See routes/invite.go.
      error.reason = payload.reason || "";
      throw error;
    }
    return payload;
  },

  // Any state-changing call carries the per-session CSRF token in the body.
  // Wrapping it here means a new endpoint cannot forget it.
  async post( path , body ) {
    return this.request( path , {
      method: "POST",
      body: Object.assign( {} , body || {} , { csrf_token: this.csrfToken } ),
    } );
  },

  async me() {
    try {
      const payload = await this.request( "/api/me" );
      this.csrfToken = payload.csrf_token;
      return payload;
    } catch ( error ) {
      if ( error.status === 401 ) return { authenticated: false };
      throw error;
    }
  },

  // The browser's local day, sent with anything that counts toward a daily
  // total or a streak. The server cannot work it out: it knows UTC, and a
  // streak that rolled over at midnight UTC would be wrong for most of the
  // world. The server sanity-checks it against its own clock.
  today() {
    const now = new Date();
    const pad = function ( value ) { return String( value ).padStart( 2 , "0" ); };
    return now.getFullYear() + "-" + pad( now.getMonth() + 1 ) + "-" + pad( now.getDate() );
  },

  rename( displayName )        { return this.post( "/api/account/rename" , { display_name: displayName } ); },
  logout()                     { return this.post( "/api/logout" ); },
  listTeam()                   { return this.request( "/api/team" ); },
  listUsers()                  { return this.request( "/api/admin/users" ).then( function ( reply ) { return reply.users; } ); },
  createUser( name , role )    { return this.post( "/api/admin/users" , { display_name: name , role: role } ); },
  reissueLogin( userId )       { return this.post( "/api/admin/users/" + userId + "/reissue-login" ); },
  setDisabled( userId , flag ) { return this.post( "/api/admin/users/" + userId + "/disabled" , { disabled: flag } ); },

  // A login link for your own account, to sign in on another device. An
  // empty body asks for the default: it works once, for a short while.
  createMyLoginLink()          { return this.post( "/api/account/login-link" ); },
  cancelMyLoginLink()          { return this.post( "/api/account/login-link/cancel" ); },

  // API keys. expiresInDays is passed through as-is, null included: the
  // server reads absent as "use the default" and 0 as "never expires".
  listKeys()                   { return this.request( "/api/keys" ).then( function ( reply ) { return reply.keys; } ); },
  createKey( name , role , expiresInDays ) {
    return this.post( "/api/keys" , { name: name , role: role , expires_in_days: expiresInDays } );
  },
  // Whether one of your own keys may make a login link for your account.
  setKeySignIn( keyId , on ) {
    return this.post( "/api/keys/" + encodeURIComponent( keyId ) + "/sign-in" , { may_sign_in: on } );
  },
  revokeKey( keyId )           { return this.post( "/api/keys/" + encodeURIComponent( keyId ) + "/revoke" ); },
  deleteKey( keyId )           { return this.post( "/api/keys/" + encodeURIComponent( keyId ) + "/delete" ); },

  // --- signing in -------------------------------------------------------
  //
  // Opening a login or invite link only shows a page; these are the requests
  // that page sends when the person confirms. Neither has a session yet, so
  // neither carries a CSRF token: the credential in the body is the
  // capability, and the server wants it from this site's own page.

  redeemLogin( credential ) {
    return this.request( "/api/login" , { method: "POST" , body: { credential: credential } } );
  },

  // --- invite links -----------------------------------------------------
  //
  // inviteState spends nothing, which is the point: the join page can ask
  // about a link as often as it likes. It is a POST only so the credential
  // stays out of URLs. claimInvite is the only call in the app that consumes
  // a seat.

  inviteState( credential ) {
    return this.request( "/api/invite/info" , { method: "POST" , body: { credential: credential } } );
  },
  claimInvite( credential , displayName ) {
    return this.request( "/api/invite/claim" , {
      method: "POST",
      body: { credential: credential , display_name: displayName },
    } );
  },
  listInvites()                { return this.request( "/api/admin/invites" ).then( function ( reply ) { return reply.invites; } ); },
  createInvite( label , role , maxUses ) {
    return this.post( "/api/admin/invites" , { label: label , role: role , max_uses: maxUses } );
  },
  revokeInvite( inviteId )     { return this.post( "/api/admin/invites/" + inviteId + "/revoke" ); },

  // --- training ---------------------------------------------------------

  // extra, when given, asks for that many new words on top of the deck,
  // ignoring the daily limit. Only the empty screen's button passes it.
  deck( extra ) {
    const more = Number( extra ) > 0 ? "&more=" + Math.floor( Number( extra ) ) : "";
    return this.request( "/api/deck?date=" + this.today() + more );
  },
  review( word , outcome )     { return this.post( "/api/review" , { word: word , outcome: outcome , date: this.today() } ); },
  cards( status )              { return this.request( "/api/cards?status=" + encodeURIComponent( status ) ); },
  setCardStatus( word , status ) { return this.post( "/api/cards/status" , { word: word , status: status } ); },
  unskip( word )               { return this.post( "/api/cards/unskip" , { word: word } ); },
  forget( word )               { return this.post( "/api/cards/forget" , { word: word } ); },
  search( query )              { return this.request( "/api/search?q=" + encodeURIComponent( query ) ); },

  getSettings()                { return this.request( "/api/settings" ); },
  saveSettings( settings )     { return this.post( "/api/settings" , { settings: settings } ); },
  levels()                     { return this.request( "/api/levels" ); },
  stats()                      { return this.request( "/api/stats?date=" + this.today() ); },
  resetProgress()              { return this.post( "/api/progress/reset" , { confirm: "reset" } ); },

  // Not a request: the URL for the spreadsheet, handed to a link so the
  // browser downloads it the ordinary way. Everything else here goes through
  // fetch, which cannot save a file -- and a blob assembled in the page and
  // clicked is exactly the thing iOS refuses.
  exportUrl()                  { return "/api/export/words.xlsx?date=" + this.today(); },
};
