// The settings screen.
//
// Option labels come from language.yaml like every other string, so the
// choices below carry a key rather than a label. A choice whose key resolves
// to "" is dropped, which is the documented way to remove one.
const Settings = {
  // The two ends of this list are the interesting ones: 0 means the answer is
  // never shown, and -1 means it stays up until the next answer. See
  // RevealHoldMs in server/models/settings.go, which is where those two
  // sentinels are defined.
  revealHoldChoices: [
    { value: 0     , key: "settings.reveal_hold_none" },
    { value: 500   , key: "settings.reveal_hold_500" },
    { value: 1000  , key: "settings.reveal_hold_1000" },
    { value: 1500  , key: "settings.reveal_hold_1500" },
    { value: 2000  , key: "settings.reveal_hold_2000" },
    { value: 3000  , key: "settings.reveal_hold_3000" },
    { value: 5000  , key: "settings.reveal_hold_5000" },
    { value: 8000  , key: "settings.reveal_hold_8000" },
    { value: 15000 , key: "settings.reveal_hold_15000" },
    { value: -1    , key: "settings.reveal_hold_until" },
  ],

  directionChoices: [
    { value: "up"    , key: "settings.swipe_up" },
    { value: "down"  , key: "settings.swipe_down" },
    { value: "left"  , key: "settings.swipe_left" },
    { value: "right" , key: "settings.swipe_right" },
  ],

  // Which select holds which outcome, in the order they appear on screen.
  swipeFields: [
    { id: "swipe-unknown" , field: "swipe_unknown" },
    { id: "swipe-skip"    , field: "swipe_skip" },
    { id: "swipe-known"   , field: "swipe_known" },
  ],

  dailyNewChoices: [ 5 , 10 , 20 , 30 , 50 , 100 , 0 ],
  batchChoices: [ 10 , 20 , 30 , 50 ],

  levels: null,
  settings: null,

  async init() {
    this.settings = await Shell.loadSettings( true );
    this.levels = await Api.levels();

    this.buildLevelSelect();
    this.buildKeyedSelect( "reveal-hold" , this.revealHoldChoices );
    const self = this;
    this.swipeFields.forEach( function ( entry ) {
      self.buildKeyedSelect( entry.id , self.directionChoices );
    } );
    this.buildNumberSelect( "daily-new" , this.dailyNewChoices , "settings.daily_new_unlimited" );
    this.buildNumberSelect( "batch-size" , this.batchChoices , "" );
    this.fill();
    this.bind();
  },

  buildLevelSelect() {
    const select = Dom.get( "level-select" );
    if ( !select || !this.levels ) return;
    Dom.clear( select );
    for ( let tier = this.levels.min; tier <= this.levels.max; tier += 1 ) {
      const option = Dom.el( "option" , {
        text: Shell.levelName( tier ),
        attrs: { value: String( tier ) },
      } );
      select.appendChild( option );
    }
  },

  // One builder for every list of {value, key} choices: the label comes from
  // language.yaml, and a key that resolves to "" drops that choice, which is
  // the documented way to remove one.
  buildKeyedSelect( id , choices ) {
    const select = Dom.get( id );
    if ( !select ) return;
    Dom.clear( select );
    choices.forEach( function ( choice ) {
      const label = I18n.get( choice.key );
      if ( label === "" ) return;
      select.appendChild( Dom.el( "option" , { text: label , attrs: { value: String( choice.value ) } } ) );
    } );
  },

  buildNumberSelect( id , choices , zeroKey ) {
    const select = Dom.get( id );
    if ( !select ) return;
    Dom.clear( select );
    choices.forEach( function ( value ) {
      let label = String( value );
      if ( value === 0 ) {
        label = I18n.get( zeroKey );
        if ( label === "" ) return;
      }
      select.appendChild( Dom.el( "option" , { text: label , attrs: { value: String( value ) } } ) );
    } );
  },

  fill() {
    const current = this.settings;
    Dom.get( "level-select" ).value = String( current.level );
    Dom.get( "reveal-hold" ).value = String( current.reveal_hold_ms );
    Dom.get( "daily-new" ).value = String( current.daily_new_limit );
    Dom.get( "batch-size" ).value = String( current.batch_size );
    Dom.get( "show-examples" ).checked = !!current.show_examples;
    Dom.get( "haptics" ).checked = !!current.haptics;
    Dom.get( "theme-dark" ).checked = current.theme === "dark";

    Dom.all( 'input[name="reveal_mode"]' ).forEach( function ( input ) {
      input.checked = input.value === current.reveal_mode;
    } );
    Dom.all( 'input[name="practice_mode"]' ).forEach( function ( input ) {
      input.checked = input.value === current.practice_mode;
    } );

    const self = this;
    this.swipeFields.forEach( function ( entry ) {
      Dom.get( entry.id ).value = current[ entry.field ];
      self.previousDirections[ entry.id ] = current[ entry.field ];
    } );

    this.showLevelCount();
    this.showUnusedDirection();
  },

  // Name the direction no answer is bound to. Without this the fourth
  // direction looks broken rather than free: a swipe that way does nothing,
  // and nothing on screen says that is deliberate.
  showUnusedDirection() {
    const label = Dom.get( "swipe-unused" );
    if ( !label ) return;
    const taken = this.swipeFields.map( function ( entry ) { return Dom.get( entry.id ).value; } );
    const spare = this.directionChoices.find( function ( choice ) {
      return taken.indexOf( choice.value ) === -1;
    } );
    if ( !spare ) { Dom.text( label , "" ); return; }
    Dom.text( label , I18n.format( "settings.swipe_unused" , {
      direction: I18n.get( "train.direction_" + spare.value ),
    } ) );
  },

  // Two answers on one direction would leave the third unreachable, so
  // picking a direction that is already taken swaps the two rather than
  // refusing the change or silently allowing a broken mapping.
  claimDirection( select ) {
    const chosen = select.value;
    const displaced = this.previousDirections[ select.id ];
    const self = this;
    this.swipeFields.forEach( function ( entry ) {
      if ( entry.id === select.id ) return;
      const other = Dom.get( entry.id );
      if ( other.value === chosen ) other.value = displaced;
      self.previousDirections[ entry.id ] = other.value;
    } );
    this.previousDirections[ select.id ] = chosen;
    this.showUnusedDirection();
  },

  // What each direction select held before the current change, which is what
  // a displaced select is given in the swap above.
  previousDirections: {},

  showLevelCount() {
    const label = Dom.get( "level-count" );
    if ( !label || !this.levels ) return;
    const tier = Number( Dom.get( "level-select" ).value );
    const counts = this.levels.counts || [];
    const total = counts[ tier - this.levels.min ] || 0;
    Dom.text( label , total.toLocaleString() + " " + I18n.get( "levels.word_count" ) );
  },

  bind() {
    const self = this;

    Dom.get( "level-select" ).addEventListener( "change" , function () { self.showLevelCount(); } );

    this.swipeFields.forEach( function ( entry ) {
      Dom.get( entry.id ).addEventListener( "change" , function ( event ) {
        self.claimDirection( event.currentTarget );
      } );
    } );

    // The theme switch applies immediately rather than on save: it is the one
    // setting whose effect is the page you are looking at, so waiting for a
    // round trip to see it would be strange.
    Dom.get( "theme-dark" ).addEventListener( "change" , function ( event ) {
      Shell.applyTheme( event.currentTarget.checked ? "dark" : "light" );
    } );

    Dom.get( "settings-form" ).addEventListener( "submit" , async function ( event ) {
      event.preventDefault();
      try {
        await self.save();
      } catch ( error ) {
        Shell.showError( error );
      }
    } );
  },

  async save() {
    const checked = function ( name , fallback ) {
      const input = Dom.all( 'input[name="' + name + '"]' ).find( function ( option ) { return option.checked; } );
      return input ? input.value : fallback;
    };

    const payload = {
      level:           Number( Dom.get( "level-select" ).value ),
      reveal_mode:     checked( "reveal_mode" , "definition_hidden" ),
      practice_mode:   checked( "practice_mode" , "mixed" ),
      swipe_unknown:   Dom.get( "swipe-unknown" ).value,
      swipe_skip:      Dom.get( "swipe-skip" ).value,
      swipe_known:     Dom.get( "swipe-known" ).value,
      reveal_hold_ms:  Number( Dom.get( "reveal-hold" ).value ),
      daily_new_limit: Number( Dom.get( "daily-new" ).value ),
      batch_size:      Number( Dom.get( "batch-size" ).value ),
      show_examples:   Dom.get( "show-examples" ).checked,
      haptics:         Dom.get( "haptics" ).checked,
      theme:           Dom.get( "theme-dark" ).checked ? "dark" : "light",
    };

    const result = await Api.saveSettings( payload );
    this.settings = result.settings;
    Shell.settings = result.settings;
    Shell.applyTheme( result.settings.theme );
    // The server normalises anything out of range, so the form is refilled
    // from what it actually stored rather than from what was sent.
    this.fill();
    Dom.flash( Dom.get( "settings-saved" ) , 2500 );
  },
};

document.addEventListener( "DOMContentLoaded" , function () {
  Shell.boot( "/settings.html" , function () { return Settings.init(); } );
} );
