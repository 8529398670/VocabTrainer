// The owner's ProjectPortal card, on the Account screen under API keys (portal
// contract v3). ProjectPortal works through a service account of its own,
// never the owner's key: Connect makes that account (when there is none by the
// name given) and one admin key for it that may make keys and never sign-in
// links, shown once. The status lists its live keys and when they expire, and
// Disconnect revokes them all, and with them everything they made.
//
// Only the owner has the card: the routes are the owner's (role=OWNER), and
// answer anyone else 404. app.js calls Portal.init( me ) once it knows who is
// signed in. Written with this app's own Dom helpers and plain markup
// (account.html), not the template's Bootstrap card.
const Portal = {
  me: null,

  async init( me ) {
    this.me = me;
    if ( me.api_keys_enabled === false || !me.is_owner ) return;
    Dom.show( Dom.get( "portal-card" ) , true );
    Dom.get( "portal-connect-form" ).addEventListener( "submit" , this.onConnect.bind( this ) );
    Dom.get( "copy-portal-key-button" ).addEventListener( "click" , function () {
      App.copyFrom( "portal-key-value" , "portal-key-copied" );
    } );
    Dom.get( "portal-disconnect-button" ).addEventListener( "click" , this.onDisconnect.bind( this ) );
    await this.refresh();
  },

  accounts: [],

  async refresh() {
    let page;
    try {
      page = await Api.request( "/api/owner/portal" );
    } catch ( error ) {
      App.showError( error.message );
      return;
    }
    this.accounts = page.accounts || [];
    const list = Dom.get( "portal-status" );
    Dom.clear( list );
    const keys = [];
    for ( const account of this.accounts ) {
      for ( const key of account.keys || [] ) keys.push( { account: account , key: key } );
    }
    if ( keys.length === 0 ) {
      list.appendChild( Dom.el( "li" , { class: "muted" , text: I18n.get( "portal.none" ) } ) );
    }
    for ( const { account , key } of keys ) {
      const when = key.expires_at
        ? I18n.get( "portal.expires" ).replace( "{date}" , () => new Date( key.expires_at ).toLocaleDateString() )
        : I18n.get( "portal.never_expires" );
      // textContent throughout: account and key names are user input.
      list.appendChild( Dom.el( "li" , { text: account.display_name + " · " + key.name + " · " + when } ) );
    }
    Dom.show( Dom.get( "portal-disconnect-button" ) , keys.length > 0 );
  },

  async onConnect( event ) {
    event.preventDefault();
    const name = Dom.get( "portal-name" ).value.trim() || "ProjectPortal";
    const days = Number( Dom.get( "portal-days" ).value );
    try {
      const created = await Api.post( "/api/owner/portal/connect" , { name: name , days: days } );
      Dom.text( Dom.get( "portal-key-value" ) , created.credential );
      Dom.show( Dom.get( "portal-key-box" ) , true );
      await this.refresh();
    } catch ( error ) {
      App.showError( error.message );
    }
  },

  async onDisconnect() {
    const question = I18n.get( "portal.disconnect_confirm" );
    if ( question !== "" && window.confirm( question ) === false ) return;
    try {
      for ( const account of this.accounts ) {
        await Api.post( "/api/owner/portal/disconnect" , { user_id: account.id } );
      }
      Dom.show( Dom.get( "portal-key-box" ) , false );
      await this.refresh();
    } catch ( error ) {
      App.showError( error.message );
    }
  },
};
