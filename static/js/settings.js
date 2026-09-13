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
    // Voices arrive asynchronously in some browsers, and a select built
    // before they land is a select with nothing in it.
    await Speech.ready();
    this.buildVoiceSelect();
    this.fill();
    this.showExport();
    this.bind();
  },

  // The voice picker, or nothing at all.
  //
  // The card is hidden outright when this device has no speech engine or no
  // English voices, rather than shown holding one dead option: a setting that
  // cannot change anything is worse than an absent one.
  //
  // A saved voice this device does not have is still listed, and listed under
  // its own name. Dropping it would mean the select silently showed something
  // else, and the next save would overwrite a choice made on another device
  // that the user never asked to change.
  buildVoiceSelect() {
    const card = Dom.get( "speech-card" );
    const select = Dom.get( "speech-voice" );
    if ( !card || !select ) return;

    const voices = Speech.supported() ? Speech.choices() : [];
    const heading = I18n.get( "settings.speech_heading" );
    if ( voices.length === 0 || heading === "" ) { Dom.show( card , false ); return; }
    Dom.show( card , true );

    Dom.clear( select );
    const auto = I18n.get( "settings.speech_voice_auto" );
    if ( auto !== "" ) select.appendChild( Dom.el( "option" , { text: auto , attrs: { value: "" } } ) );

    const saved = this.settings.speech_voice || "";
    let listed = false;
    voices.forEach( function ( voice ) {
      if ( voice.name === saved ) listed = true;
      // "Arthur" does not say which English it speaks, so the tag is worth
      // the room. Some platforms have already said it -- macOS reports
      // "Daniel (English (United Kingdom))" -- and adding a second bracket
      // to those is noise, so the tag goes on only where there is not one.
      const qualified = voice.name.indexOf( "(" ) !== -1;
      select.appendChild( Dom.el( "option" , {
        text: qualified ? voice.name : voice.name + " (" + voice.lang + ")",
        attrs: { value: voice.name },
      } ) );
    } );
    if ( saved !== "" && listed === false ) {
      select.appendChild( Dom.el( "option" , { text: saved , attrs: { value: saved } } ) );
    }
  },

  // The download link, and whether there is one.
  //
  // The whole card goes when its heading does, rather than each label hiding
  // itself and leaving an empty bordered box behind -- language.yaml's rule
  // is "an empty key removes what it names", and what this key names is the
  // feature. The href carries the browser's local date so the saved file is
  // named for the user's day; the server only uses it for the filename, and
  // sanity-checks it against its own clock either way.
  showExport() {
    Dom.show( Dom.get( "export-card" ) , I18n.get( "export.heading" ) !== "" );
    const link = Dom.get( "export-xlsx" );
    if ( link ) link.setAttribute( "href" , Api.exportUrl() );
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
    Dom.get( "known-skips-reveal" ).checked = !!current.known_skips_reveal;
    Dom.get( "speech-voice" ).value = current.speech_voice || "";
    Dom.get( "show-examples" ).checked = !!current.show_examples;
    Dom.get( "hide-level-badge" ).checked = !!current.hide_level_badge;
    Dom.get( "haptics" ).checked = !!current.haptics;
    Dom.get( "theme-dark" ).checked = current.theme === "dark";

    Dom.all( 'input[name="reveal_mode"]' ).forEach( function ( input ) {
      input.checked = input.value === current.reveal_mode;
    } );
    Dom.all( 'input[name="practice_mode"]' ).forEach( function ( input ) {
      input.checked = input.value === current.practice_mode;
    } );
    Dom.all( 'input[name="scoring_mode"]' ).forEach( function ( input ) {
      input.checked = input.value === current.scoring_mode;
    } );

    const self = this;
    this.swipeFields.forEach( function ( entry ) {
      Dom.get( entry.id ).value = current[ entry.field ];
      self.previousDirections[ entry.id ] = current[ entry.field ];
    } );

    this.showLevelCount();
    this.showUnusedDirection();
    this.showModeNotes();
  },

  // The two notes that only apply to one choice each. Both follow
  // language.yaml's rule rather than only the radio: an empty key removes the
  // note, and unhiding an element whose text was removed would put an empty
  // paragraph on screen.
  //
  // They track the radio rather than what is saved, like the theme switch and
  // the voice preview below: a note that only appeared after saving would be
  // explaining a decision the user has already made.
  showModeNotes() {
    const picked = function ( name ) {
      const input = Dom.all( 'input[name="' + name + '"]' ).find( function ( option ) { return option.checked; } );
      return input ? input.value : "";
    };
    const note = function ( id , key , when ) {
      const element = Dom.get( id );
      if ( !element ) return;
      Dom.show( element , when && I18n.get( key ) !== "" );
    };

    // The unused-direction line and the graded note answer the same question
    // -- what the directions actually do -- so only one of them is on screen
    // at a time.
    const graded = picked( "scoring_mode" ) === "graded";
    note( "swipe-graded-note" , "settings.swipe_graded_note" , graded );
    Dom.show( Dom.get( "swipe-unused" ) , !graded );
    note( "practice-new-note" , "settings.practice_new_note" , picked( "practice_mode" ) === "new_only" );
  },

  // Name the direction no answer is bound to. Without this the fourth
  // direction looks broken rather than free: a swipe that way does nothing,
  // and nothing on screen says that is deliberate.
  showUnusedDirection() {
    const label = Dom.get( "swipe-unused" );
    if ( !label ) return;
    // Only the text: whether the line is on screen at all belongs to
    // showModeNotes, which hands the floor to the graded note instead. Keeping
    // the two apart means this line is still up to date when it comes back.
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

    Dom.all( 'input[name="scoring_mode"], input[name="practice_mode"]' ).forEach( function ( input ) {
      input.addEventListener( "change" , function () { self.showModeNotes(); } );
    } );

    // Hearing the voice is the only way to choose one, so the preview speaks
    // whatever is selected right now rather than what is saved. Same reason
    // the theme switch below applies before the save does.
    const preview = Dom.get( "speech-preview" );
    if ( preview ) {
      preview.addEventListener( "click" , function () {
        const sample = I18n.get( "settings.speech_sample" );
        if ( sample === "" ) return;
        Speech.speak( sample , { voiceName: Dom.get( "speech-voice" ).value } );
      } );
    }

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
      scoring_mode:    checked( "scoring_mode" , "binary" ),
      swipe_unknown:   Dom.get( "swipe-unknown" ).value,
      swipe_skip:      Dom.get( "swipe-skip" ).value,
      swipe_known:     Dom.get( "swipe-known" ).value,
      reveal_hold_ms:  Number( Dom.get( "reveal-hold" ).value ),
      known_skips_reveal: Dom.get( "known-skips-reveal" ).checked,
      speech_voice:    Dom.get( "speech-voice" ).value,
      daily_new_limit: Number( Dom.get( "daily-new" ).value ),
      batch_size:      Number( Dom.get( "batch-size" ).value ),
      show_examples:   Dom.get( "show-examples" ).checked,
      hide_level_badge: Dom.get( "hide-level-badge" ).checked,
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
