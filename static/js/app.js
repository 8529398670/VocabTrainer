// Page logic for the account + admin screen this template ships with.
//
// Most projects will replace much of this file. What is worth keeping is the
// shape: load the language file, ask /api/me once, then branch on
// authenticated/role. Do not re-check the session anywhere else -- the server
// is the authority, and a second client-side notion of "am I logged in" is
// how the two drift apart.
//
// New screens belong in their own file under /js/ with their own init(),
// rather than being appended here until this becomes the monolith the rest of
// the project is organised to avoid.

const App = {
  currentUser: null,

  // Shell.boot has already loaded the language file, confirmed the session
  // and revealed the page by the time this runs, so this file only has to
  // render the account screen. Nothing here re-checks whether the user is
  // signed in -- the server is the authority, and a second client-side
  // notion of "am I logged in" is how the two drift apart.
  async init() {
    await this.renderSignedIn( Shell.user );
  },

  async renderSignedIn( me ) {
    this.currentUser = me;
    Dom.text( Dom.get( "who-am-i" ) , me.display_name + " (" + me.role + ")" );
    Dom.get( "display-name" ).value = me.display_name;

    Dom.get( "rename-form" ).addEventListener( "submit" , this.onRename.bind( this ) );
    Dom.get( "logout-button" ).addEventListener( "click" , this.onLogout.bind( this ) );

    if ( me.role !== "admin" ) return;

    Dom.show( Dom.get( "admin-panel" ) , true );
    Dom.get( "create-user-form" ).addEventListener( "submit" , this.onCreateUser.bind( this ) );
    Dom.get( "copy-link-button" ).addEventListener( "click" , this.onCopyLink.bind( this ) );
    await this.refreshUsers();
  },

  async onRename( event ) {
    event.preventDefault();
    const value = Dom.get( "display-name" ).value.trim();
    try {
      await Api.rename( value );
      this.currentUser.display_name = value;
      Dom.text( Dom.get( "who-am-i" ) , value + " (" + this.currentUser.role + ")" );
      Dom.flash( Dom.get( "rename-saved" ) );
    } catch ( error ) {
      this.showError( error.message );
    }
  },

  async onLogout() {
    try {
      await Api.logout();
    } catch ( error ) {
      // Even if the call failed, reloading lands on the signed-out page,
      // which is the honest state to show.
    }
    window.location.href = "/";
  },

  async onCreateUser( event ) {
    event.preventDefault();
    const name = Dom.get( "new-name" ).value.trim();
    const role = Dom.get( "new-role" ).value;
    try {
      const created = await Api.createUser( name , role );
      this.showLoginLink( created.login_path );
      Dom.get( "new-name" ).value = "";
      await this.refreshUsers();
    } catch ( error ) {
      this.showError( error.message );
    }
  },

  // The server returns a path, not a full URL: behind a reverse proxy it does
  // not reliably know its own public origin, whereas the browser always does.
  showLoginLink( loginPath ) {
    Dom.text( Dom.get( "login-link-value" ) , window.location.origin + loginPath );
    Dom.show( Dom.get( "login-link-box" ) , true );
  },

  async onCopyLink() {
    const value = Dom.get( "login-link-value" ).textContent;
    try {
      await navigator.clipboard.writeText( value );
      Dom.flash( Dom.get( "copied-notice" ) );
    } catch ( error ) {
      // clipboard access needs a secure context, so plain-http local runs
      // land here. Selecting the text is a serviceable fallback.
      const range = document.createRange();
      range.selectNodeContents( Dom.get( "login-link-value" ) );
      const selection = window.getSelection();
      selection.removeAllRanges();
      selection.addRange( range );
    }
  },

  async refreshUsers() {
    const body = Dom.get( "users-table" ).querySelector( "tbody" );
    let users = [];
    try {
      users = await Api.listUsers();
    } catch ( error ) {
      this.showError( error.message );
      return;
    }

    Dom.clear( body );
    users.forEach( function ( user ) {
      const statusKey = user.disabled ? "admin.status_disabled" : "admin.status_active";

      const reissueButton = Dom.el( "button" , {
        class: "secondary small",
        text: I18n.get( "admin.reissue_button" ),
        on: { click: async function () {
          try {
            const issued = await Api.reissueLogin( user.id );
            App.showLoginLink( issued.login_path );
          } catch ( error ) { App.showError( error.message ); }
        } },
      } );

      const toggleButton = Dom.el( "button" , {
        class: "secondary small",
        text: I18n.get( user.disabled ? "admin.enable_button" : "admin.disable_button" ),
        on: { click: async function () {
          try {
            await Api.setDisabled( user.id , !user.disabled );
            await App.refreshUsers();
          } catch ( error ) { App.showError( error.message ); }
        } },
      } );

      // Built with createElement + textContent, never an HTML string: a
      // display name is user-controlled input.
      body.appendChild( Dom.el( "tr" , { children: [
        Dom.el( "td" , { text: user.display_name } ),
        Dom.el( "td" , { text: user.role } ),
        Dom.el( "td" , { text: I18n.get( statusKey ) } ),
        Dom.el( "td" , { children: [
          Dom.el( "div" , { class: "row-actions" , children: [ reissueButton , toggleButton ] } ),
        ] } ),
      ] } ) );
    } );
  },

  showError( message ) {
    const banner = Dom.get( "error-banner" );
    Dom.text( banner , message || I18n.get( "errors.generic" ) );
    Dom.flash( banner , 5000 );
  },
};

document.addEventListener( "DOMContentLoaded" , function () {
  Shell.boot( "/account.html" , function () { return App.init(); } );
} );
