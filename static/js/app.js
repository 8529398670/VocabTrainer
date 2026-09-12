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

    Dom.show( Dom.get( "invite-panel" ) , true );
    Dom.get( "create-invite-form" ).addEventListener( "submit" , this.onCreateInvite.bind( this ) );
    Dom.get( "copy-invite-button" ).addEventListener( "click" , this.onCopyInvite.bind( this ) );

    await this.refreshUsers();
    await this.refreshInvites();
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
    await this.copyFrom( "login-link-value" , "copied-notice" );
  },

  // Shared by both copy buttons, so the secure-context fallback below has one
  // implementation rather than one per kind of link.
  async copyFrom( valueId , noticeId ) {
    const source = Dom.get( valueId );
    try {
      await navigator.clipboard.writeText( source.textContent );
      Dom.flash( Dom.get( noticeId ) );
    } catch ( error ) {
      // clipboard access needs a secure context, so plain-http local runs
      // land here. Selecting the text is a serviceable fallback.
      const range = document.createRange();
      range.selectNodeContents( source );
      const selection = window.getSelection();
      selection.removeAllRanges();
      selection.addRange( range );
    }
  },

  // --- invite links -----------------------------------------------------

  async onCreateInvite( event ) {
    event.preventDefault();
    const label = Dom.get( "invite-label" ).value.trim();
    const role = Dom.get( "invite-role" ).value;
    const uses = parseInt( Dom.get( "invite-uses" ).value , 10 );
    try {
      const created = await Api.createInvite( label , role , uses );
      this.showInviteLink( created );
      Dom.get( "invite-label" ).value = "";
      await this.refreshInvites();
    } catch ( error ) {
      this.showError( error.message );
    }
  },

  // Like a login link, the server hands back a path and the browser supplies
  // the origin -- behind a reverse proxy the server does not reliably know
  // its own, and a confidently wrong host is worse than no host. The notice
  // states the seat count, because an invite for 3 and an invite for 30 look
  // identical once it is just a URL on the clipboard.
  showInviteLink( created ) {
    Dom.text( Dom.get( "invite-link-notice" ) ,
      I18n.format( "invites.link_notice" , {
        uses: created.max_uses,
        days: Math.max( 1 , Math.round( created.expires_in_seconds / 86400 ) ),
      } ) );
    Dom.text( Dom.get( "invite-link-value" ) , window.location.origin + created.join_path );
    Dom.show( Dom.get( "invite-link-box" ) , true );
  },

  async onCopyInvite() {
    await this.copyFrom( "invite-link-value" , "invite-copied-notice" );
  },

  async refreshInvites() {
    const body = Dom.get( "invites-table" ).querySelector( "tbody" );
    let invites = [];
    try {
      invites = await Api.listInvites();
    } catch ( error ) {
      this.showError( error.message );
      return;
    }

    Dom.clear( body );
    Dom.show( Dom.get( "invites-empty" ) , invites.length === 0 );

    invites.forEach( function ( invite ) {
      // "used / total", which is the number an admin actually wants: how
      // many places are still going, at a glance.
      const uses = I18n.format( "invites.uses_value" , {
        used: invite.used_count,
        total: invite.max_uses,
      } );
      const status = invite.usable
        ? I18n.get( "invites.status_live" )
        : I18n.get( "invites.status_" + invite.reason );

      // Only a live link can be withdrawn. One already full, expired or
      // revoked has nothing left to take back, so the button is left off
      // rather than shown doing nothing.
      const actions = [];
      if ( invite.usable ) {
        actions.push( Dom.el( "button" , {
          class: "secondary small",
          text: I18n.get( "invites.revoke_button" ),
          on: { click: async function () {
            if ( window.confirm( I18n.get( "invites.revoke_confirm" ) ) === false ) return;
            try {
              await Api.revokeInvite( invite.id );
              await App.refreshInvites();
            } catch ( error ) { App.showError( error.message ); }
          } },
        } ) );
      }

      // createElement + textContent throughout: the label is admin-written,
      // but it is still stored input and gets the same treatment as a
      // display name. See the note at the top of dom.js.
      body.appendChild( Dom.el( "tr" , { children: [
        Dom.el( "td" , { text: invite.label || I18n.get( "invites.no_label" ) } ),
        Dom.el( "td" , { text: uses } ),
        Dom.el( "td" , { text: invite.role } ),
        Dom.el( "td" , { text: status } ),
        Dom.el( "td" , { children: [
          Dom.el( "div" , { class: "row-actions" , children: actions } ),
        ] } ),
      ] } ) );
    } );
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
