// The join screen, behind /join/<credential>.
//
// This is the only page in the app a stranger ever loads, and the only one
// that can create an account. It does not use Shell.boot(): boot's whole job
// is to branch on signed-in versus signed-out and render the shared chrome,
// and a visitor holding an invite is a third case with no tab bar and nothing
// of their own to show yet.
//
// Loading this page is free. Nothing here claims a seat until the form is
// submitted -- see server/routes/invite.go for why that separation is what
// makes a link survive being posted to a group chat.
const Join = {
  credential: null,

  // The credential is the rest of the path after /join/. Taken from
  // pathname rather than a query string so it is never logged as a
  // parameter and never lands in a Referer -- the app sends
  // Referrer-Policy: same-origin, so it does not leave the origin at all.
  readCredential() {
    const path = window.location.pathname.replace( /^\/+join\/+/ , "" );
    return path.replace( /\/+$/ , "" );
  },

  async init() {
    this.credential = this.readCredential();

    try {
      await I18n.load();
    } catch ( loadError ) {
      // No language file means no text to phrase an error in. Leaving the
      // page blank is more honest than inventing an untranslated string.
      return;
    }

    Shell.applyStoredTheme();
    I18n.apply();
    Dom.show( Dom.get( "main" ) , true );

    // Asked before anything else: someone who already has an account and
    // taps the link in the group chat should not spend a seat on a second
    // empty one. The claim endpoint refuses it too -- this is just the
    // version of that answer with an explanation attached.
    let identity = { authenticated: false };
    try {
      identity = await Api.me();
    } catch ( meError ) {
      identity = { authenticated: false };
    }
    if ( identity && identity.authenticated ) {
      Dom.text( Dom.get( "join-already-body" ) ,
        I18n.format( "join.already_body" , { name: identity.display_name } ) );
      Dom.show( Dom.get( "join-already-card" ) , true );
      return;
    }

    if ( this.credential === "" ) {
      this.refuse( "invalid" );
      return;
    }

    let state = null;
    try {
      state = await Api.inviteState( this.credential );
    } catch ( stateError ) {
      this.showError( stateError );
      return;
    }

    if ( !state.usable ) {
      this.refuse( state.reason );
      return;
    }

    Dom.text( Dom.get( "join-seats" ) ,
      I18n.format( "join.seats_left" , {
        seats: state.seats_left,
        total: state.max_uses,
      } ) );
    Dom.get( "join-form" ).addEventListener( "submit" , this.onSubmit.bind( this ) );
    Dom.show( Dom.get( "join-form-card" ) , true );
    Dom.get( "join-name" ).focus();
  },

  // Every refusal renders the same card with a different line, so a reason
  // the server grows later shows up as a missing string rather than a
  // missing branch.
  refuse( reason ) {
    const message = I18n.get( "join.refused_" + ( reason || "invalid" ) )
      || I18n.get( "join.refused_invalid" );
    Dom.text( Dom.get( "join-refused-reason" ) , message );
    Dom.show( Dom.get( "join-refused-card" ) , true );
  },

  async onSubmit( event ) {
    event.preventDefault();
    const name = Dom.get( "join-name" ).value.trim();
    if ( name === "" ) return;

    // Disabled for the duration: this is the one request in the app that
    // consumes something finite, and a double-tap on a slow connection
    // would otherwise spend two seats on one person.
    const submit = Dom.get( "join-submit" );
    submit.disabled = true;

    try {
      await Api.claimInvite( this.credential , name );
    } catch ( claimError ) {
      submit.disabled = false;
      // A seat that went while the form was open is not an error to report
      // as one -- it is the link's normal end, and the refusal card says so.
      if ( claimError.reason && claimError.reason !== "" ) {
        Dom.show( Dom.get( "join-form-card" ) , false );
        this.refuse( claimError.reason );
        return;
      }
      this.showError( claimError );
      return;
    }

    Dom.show( Dom.get( "join-form-card" ) , false );
    Dom.show( Dom.get( "join-done-card" ) , true );
    // A full load rather than rendering the app here: the session cookie is
    // set, and every other screen boots by asking /api/me who it belongs to.
    window.location.href = "/";
  },

  showError( error ) {
    const banner = Dom.get( "error-banner" );
    const message = error && error.message ? error.message : I18n.get( "errors.generic" );
    Dom.text( banner , message );
    Dom.show( banner , true );
  },
};

document.addEventListener( "DOMContentLoaded" , function () {
  Join.init();
} );
