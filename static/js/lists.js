// The three word lists, plus a lookup box.
//
// Everything a swipe does is reversible from here: a word filed as unknown
// can be moved to known, anything can be skipped, and a skipped word can be
// put back where it came from. That is the recovery path for a mis-swipe,
// which on a gesture-driven screen is not a rare event.
const Lists = {
  status: "unknown",
  searching: false,

  tabs: [
    { status: "unknown" , key: "lists.tab_unknown" },
    { status: "known"   , key: "lists.tab_known" },
    { status: "skipped" , key: "lists.tab_skipped" },
  ],

  async init() {
    await Shell.loadSettings();
    this.renderTabs();
    this.bindSearch();
    await this.load();
  },

  renderTabs() {
    const container = Dom.get( "list-tabs" );
    if ( !container ) return;
    Dom.clear( container );
    const self = this;
    this.tabs.forEach( function ( tab ) {
      const button = Dom.el( "button" , {
        attrs: { type: "button" , role: "tab" , "data-i18n": tab.key },
        on: { click: function () { self.select( tab.status ); } },
      } );
      button.setAttribute( "aria-selected" , tab.status === self.status ? "true" : "false" );
      container.appendChild( button );
    } );
    I18n.apply( container );
  },

  async select( status ) {
    this.status = status;
    this.searching = false;
    const input = Dom.get( "search-input" );
    if ( input ) input.value = "";
    this.renderTabs();
    await this.load();
  },

  bindSearch() {
    const form = Dom.get( "search-form" );
    const input = Dom.get( "search-input" );
    if ( !form || !input ) return;
    const self = this;

    form.addEventListener( "submit" , function ( event ) {
      event.preventDefault();
      self.runSearch( input.value );
    } );

    // Search as you type, but only once typing pauses -- one request per
    // keystroke would be a request per keystroke.
    let timer = null;
    input.addEventListener( "input" , function () {
      window.clearTimeout( timer );
      const value = input.value;
      timer = window.setTimeout( function () { self.runSearch( value ); } , 280 );
    } );
  },

  async runSearch( query ) {
    const trimmed = String( query || "" ).trim();
    if ( trimmed === "" ) {
      this.searching = false;
      await this.load();
      return;
    }
    try {
      const payload = await Api.search( trimmed );
      this.searching = true;
      this.renderCards( payload.cards || [] , "" );
    } catch ( error ) {
      Shell.showError( error );
    }
  },

  async load() {
    try {
      const payload = await Api.cards( this.status );
      this.renderCards( payload.cards || [] , "lists.empty_" + this.status );
    } catch ( error ) {
      Shell.showError( error );
    }
  },

  renderCards( cards , emptyKey ) {
    const list = Dom.get( "word-list" );
    const empty = Dom.get( "list-empty" );
    const count = Dom.get( "list-count" );
    if ( !list ) return;

    Dom.clear( list );

    if ( count ) {
      const unit = I18n.get( cards.length === 1 ? "lists.count_one" : "lists.count_many" );
      Dom.text( count , cards.length + " " + unit );
    }

    if ( cards.length === 0 ) {
      Dom.text( empty , emptyKey ? I18n.get( emptyKey ) : "" );
      Dom.show( empty , true );
      return;
    }
    Dom.show( empty , false );

    const self = this;
    cards.forEach( function ( card ) { list.appendChild( self.buildRow( card ) ); } );
  },

  buildRow( card ) {
    const senses = card.senses || [];
    const primary = senses[ 0 ] || { definition: "" };

    const main = Dom.el( "div" , { class: "word-row-main" , children: [
      Dom.el( "div" , { class: "word-row-head" , children: [
        Dom.el( "span" , { class: "word-row-word" , text: card.word } ),
        Dom.el( "span" , { class: "badge" , text: Shell.levelName( card.tier ) } ),
      ] } ),
      Dom.el( "p" , { class: "word-row-definition" , text: primary.definition } ),
    ] } );

    if ( card.lapses > 0 ) {
      const label = card.lapses === 1
        ? I18n.get( "lists.lapses_one" )
        : card.lapses + " " + I18n.get( "lists.lapses_many" );
      main.querySelector( ".word-row-head" ).appendChild(
        Dom.el( "span" , { class: "badge" , text: label } ) );
    }

    return Dom.el( "div" , { class: "word-row" , children: [ main , this.buildActions( card ) ] } );
  },

  // Which actions a row offers depends on the list it is in: offering "know
  // it" on a word already in the known list would do nothing visible.
  buildActions( card ) {
    const actions = Dom.el( "div" , { class: "word-row-actions" } );
    const self = this;

    const add = function ( labelKey , handler , className ) {
      const label = I18n.get( labelKey );
      if ( label === "" ) return;
      actions.appendChild( Dom.el( "button" , {
        class: className || "secondary",
        text: label,
        attrs: { type: "button" },
        on: { click: function ( event ) { self.run( event.currentTarget , handler ); } },
      } ) );
    };

    if ( card.status === "skipped" ) {
      add( "lists.action_unskip" , function () { return Api.unskip( card.word ); } , "action-known" );
      add( "lists.action_known" , function () { return Api.setCardStatus( card.word , "known" ); } );
      return actions;
    }

    if ( card.status !== "known" ) {
      add( "lists.action_known" , function () { return Api.setCardStatus( card.word , "known" ); } , "action-known" );
    }
    if ( card.status !== "unknown" ) {
      add( "lists.action_unknown" , function () { return Api.setCardStatus( card.word , "unknown" ); } );
    }
    add( "lists.action_skip" , function () { return Api.setCardStatus( card.word , "skipped" ); } );
    if ( card.status ) {
      add( "lists.action_forget" , function () { return Api.forget( card.word ); } );
    }
    return actions;
  },

  // Buttons are disabled while their request is in flight. Without it a
  // double tap sends the change twice, and on a slow connection the list
  // reloads underneath the second one.
  async run( button , handler ) {
    if ( button.disabled ) return;
    button.disabled = true;
    try {
      await handler();
      if ( this.searching ) {
        const input = Dom.get( "search-input" );
        await this.runSearch( input ? input.value : "" );
      } else {
        await this.load();
      }
    } catch ( error ) {
      Shell.showError( error );
      button.disabled = false;
    }
  },
};

document.addEventListener( "DOMContentLoaded" , function () {
  Shell.boot( "/lists.html" , function () { return Lists.init(); } );
} );
