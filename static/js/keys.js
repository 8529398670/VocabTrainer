// The API keys card on the Account screen: make a key, see it once, and
// revoke, delete or let it make sign-in links later. A key acts as your own
// account at no more than your role; the server enforces all of that, so this
// file only draws what it is told.
const Keys = {
  me: null,

  async init( me ) {
    this.me = me;
    if ( !me.api_keys_enabled ) return;

    Dom.show( Dom.get( "keys-panel" ) , true );
    if ( me.api_doc_url ) Dom.get( "key-doc-link" ).href = me.api_doc_url;
    // Only an admin can make an admin key, so only an admin is offered one.
    Dom.show( Dom.get( "key-role-field" ) , me.role === "admin" );
    Dom.get( "create-key-form" ).addEventListener( "submit" , this.onCreate.bind( this ) );
    Dom.get( "key-copy-button" ).addEventListener( "click" , function () {
      App.copyFrom( "key-secret-value" , "key-copied" );
    } );
    await this.refresh();
  },

  async onCreate( event ) {
    event.preventDefault();
    const name = Dom.get( "key-name" ).value.trim();
    const role = this.me.role === "admin" ? Dom.get( "key-role" ).value : "user";
    // Blank means the server's default lifetime; 0 means never.
    const daysText = Dom.get( "key-days" ).value.trim();
    const days = daysText === "" ? null : parseInt( daysText , 10 );
    try {
      const created = await Api.createKey( name , role , days );
      Dom.text( Dom.get( "key-secret-value" ) , created.credential );
      Dom.show( Dom.get( "key-secret-box" ) , true );
      Dom.get( "key-name" ).value = "";
      Dom.get( "key-days" ).value = "";
      await this.refresh();
    } catch ( error ) {
      App.showError( error.message );
    }
  },

  // A date the way the reader's own locale writes it, or "never".
  when( value ) {
    if ( !value ) return I18n.get( "api_keys.never" );
    return new Date( value ).toLocaleDateString();
  },

  async refresh() {
    const body = Dom.get( "keys-table" ).querySelector( "tbody" );
    let keys = [];
    try {
      keys = await Api.listKeys();
    } catch ( error ) {
      App.showError( error.message );
      return;
    }

    Dom.clear( body );
    Dom.show( Dom.get( "keys-empty" ) , keys.length === 0 );

    keys.forEach( function ( key ) {
      let status = key.revoked ? "revoked" : ( key.expired ? "expired" : "active" );
      let statusText = I18n.get( "api_keys.status_" + status );
      if ( key.live && key.may_sign_in ) statusText += " · " + I18n.get( "api_keys.status_sign_in" );

      const actions = [];
      if ( key.live ) {
        actions.push( Dom.el( "button" , {
          class: "secondary small",
          text: I18n.get( key.may_sign_in ? "api_keys.sign_in_on_button" : "api_keys.sign_in_off_button" ),
          on: { click: async function () {
            const turningOn = !key.may_sign_in;
            const question = I18n.get( "api_keys.sign_in_confirm" );
            if ( turningOn && question !== "" && window.confirm( question ) === false ) return;
            try {
              await Api.setKeySignIn( key.id , turningOn );
              await Keys.refresh();
            } catch ( error ) { App.showError( error.message ); }
          } },
        } ) );
        actions.push( Dom.el( "button" , {
          class: "secondary small",
          text: I18n.get( "api_keys.revoke_button" ),
          on: { click: async function () {
            const question = I18n.get( "api_keys.revoke_confirm" );
            if ( question !== "" && window.confirm( question ) === false ) return;
            try {
              await Api.revokeKey( key.id );
              await Keys.refresh();
            } catch ( error ) { App.showError( error.message ); }
          } },
        } ) );
      } else {
        // A dead key only clutters the list; deleting it is the tidy-up.
        actions.push( Dom.el( "button" , {
          class: "secondary small",
          text: I18n.get( "api_keys.delete_button" ),
          on: { click: async function () {
            try {
              await Api.deleteKey( key.id );
              await Keys.refresh();
            } catch ( error ) { App.showError( error.message ); }
          } },
        } ) );
      }

      // textContent throughout: a key's name is user input.
      body.appendChild( Dom.el( "tr" , { children: [
        Dom.el( "td" , { text: key.name } ),
        Dom.el( "td" , { text: key.role } ),
        Dom.el( "td" , { text: statusText } ),
        Dom.el( "td" , { text: Keys.when( key.last_used_at ) } ),
        Dom.el( "td" , { text: Keys.when( key.expires_at ) } ),
        Dom.el( "td" , { children: [
          Dom.el( "div" , { class: "row-actions" , children: actions } ),
        ] } ),
      ] } ) );
    } );
  },
};
