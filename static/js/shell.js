// Shared page chrome: boot sequence, the tab bar, and the signed-in check.
//
// Every screen calls Shell.boot() with its own init function. That keeps the
// order of the three things each page needs -- text, identity, theme --
// in one place rather than repeated (and eventually diverging) five times.
const Shell = {
  user: null,
  settings: null,

  // The tab bar. Defined once here rather than copied into five HTML files,
  // so adding a screen is a one-line change. Labels come from language.yaml
  // like every other string; the icons are text, not an icon font, because a
  // font would be a dependency and the CSP would have to allow it.
  tabs: [
    { href: "/",             key: "nav.train",    icon: "◆" },
    { href: "/lists.html",   key: "nav.lists",    icon: "≡" },
    { href: "/stats.html",   key: "nav.stats",    icon: "◴" },
    { href: "/settings.html",key: "nav.settings", icon: "⚙" },
    { href: "/account.html", key: "nav.account",  icon: "○" },
  ],

  // Theme is applied before anything is fetched, from the copy kept in
  // localStorage. The authoritative value lives on the server with the rest
  // of the settings, but waiting for it would mean a light flash on every
  // page load for anyone using dark mode.
  applyStoredTheme() {
    let theme = "light";
    try {
      theme = window.localStorage.getItem( "theme" ) || "light";
    } catch ( storageError ) {
      theme = "light";
    }
    this.applyTheme( theme );
  },

  applyTheme( theme ) {
    const value = theme === "dark" ? "dark" : "light";
    document.documentElement.setAttribute( "data-theme" , value );
    try {
      window.localStorage.setItem( "theme" , value );
    } catch ( storageError ) {
      // Private browsing, or storage disabled. The theme still applies for
      // this page load; only the head start on the next one is lost.
    }
  },

  renderTabs( currentPath ) {
    const bar = Dom.get( "tabbar" );
    if ( !bar ) return;
    Dom.clear( bar );
    this.tabs.forEach( function ( tab ) {
      const link = Dom.el( "a" , {
        attrs: { href: tab.href },
        children: [
          Dom.el( "span" , { class: "tab-icon" , text: tab.icon } ),
          Dom.el( "span" , { attrs: { "data-i18n": tab.key } } ),
        ],
      } );
      if ( tab.href === currentPath ) link.setAttribute( "aria-current" , "page" );
      bar.appendChild( link );
    } );
  },

  // boot runs the same four steps on every page and then hands over.
  //
  // init is only called when someone is actually signed in, so no screen has
  // to begin by re-checking. Signed-out visitors get the shared explanation
  // and no tab bar.
  async boot( currentPath , init ) {
    this.applyStoredTheme();

    try {
      await I18n.load();
    } catch ( loadError ) {
      // Without language.yaml there is no text to show an error in, so the
      // page is left as it is rather than inventing an untranslated string.
      return;
    }

    let identity = { authenticated: false };
    try {
      identity = await Api.me();
    } catch ( meError ) {
      identity = { authenticated: false };
    }

    const signedIn = !!( identity && identity.authenticated );
    if ( signedIn ) {
      this.user = identity;
      this.renderTabs( currentPath );
    }

    I18n.apply();
    Dom.show( Dom.get( "main" ) , true );
    Dom.show( Dom.get( "tabbar" ) , !!signedIn );
    Dom.show( Dom.get( "signed-out" ) , !signedIn );
    Dom.show( Dom.get( "signed-in" ) , !!signedIn );

    if ( !signedIn ) {
      // The login route redirects here with ?login=invalid rather than
      // explaining what was wrong with the link -- see routes/auth.go.
      const params = new URLSearchParams( window.location.search );
      if ( params.get( "login" ) === "invalid" && I18n.get( "signed_out.invalid_link" ) !== "" ) {
        Dom.show( Dom.get( "invalid-link" ) , true );
      }
      return;
    }

    const who = Dom.get( "who-am-i" );
    if ( who ) Dom.text( who , this.user.display_name );

    try {
      await init();
    } catch ( initError ) {
      this.showError( initError );
    }
  },

  // Settings are needed by more than one screen, so they are fetched once and
  // cached on the shell rather than by each caller.
  async loadSettings( force ) {
    if ( this.settings && !force ) return this.settings;
    const payload = await Api.getSettings();
    this.settings = payload.settings;
    this.applyTheme( this.settings.theme );
    return this.settings;
  },

  levelName( tier ) {
    return I18n.get( "levels.tier_" + tier ) || String( tier );
  },

  showError( error ) {
    const banner = Dom.get( "error-banner" );
    if ( !banner ) return;
    const message = error && error.message
      ? error.message
      : I18n.get( "errors.generic" );
    Dom.text( banner , message );
    Dom.show( banner , true );
    window.clearTimeout( banner._hideTimer );
    banner._hideTimer = window.setTimeout( function () { banner.hidden = true; } , 5000 );
  },

  // A short buzz on a decisive action, when the setting allows it and the
  // device has the hardware. Wrapped because desktop Safari throws rather
  // than ignoring it.
  buzz( milliseconds ) {
    if ( !this.settings || !this.settings.haptics ) return;
    try {
      if ( navigator.vibrate ) navigator.vibrate( milliseconds || 12 );
    } catch ( vibrateError ) {
      // No haptics here. Not worth reporting.
    }
  },
};
