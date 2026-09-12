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
  listUsers()                  { return this.request( "/api/admin/users" ); },
  createUser( name , role )    { return this.post( "/api/admin/users" , { display_name: name , role: role } ); },
  reissueLogin( userId )       { return this.post( "/api/admin/users/" + userId + "/reissue-login" ); },
  setDisabled( userId , flag ) { return this.post( "/api/admin/users/" + userId + "/disabled" , { disabled: flag } ); },

  // --- training ---------------------------------------------------------

  deck()                       { return this.request( "/api/deck?date=" + this.today() ); },
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
};
