// Saying a word out loud, using whatever voice the device already has.
//
// This is the browser's own speechSynthesis -- the same engine the operating
// system uses for VoiceOver and TalkBack. Nothing is downloaded, nothing is
// sent anywhere, and there is no audio file to serve: the word never leaves
// the device. That is the whole reason to use it rather than a pronunciation
// API, and it is why the button simply is not built on a browser that has no
// speech engine, instead of being shown and then doing nothing.
//
// Two platform facts shape the code below.
//
//   * getVoices() is empty on the first call in Chrome and fills in
//     asynchronously. Waiting for it inside a click handler would be worse
//     than useless -- see speak() -- so the list is warmed up at load time
//     and whatever is available when the button is pressed is what gets used.
//   * iOS only speaks from inside a user gesture. Everything here runs
//     directly in a click handler with no await before the speak() call, so
//     the gesture is still live when the utterance is queued.
const Speech = {
  // The corpus is English whatever language the interface has been set to --
  // it is a list of English words with English definitions -- so the voice is
  // chosen for the words, not for the UI.
  LANGUAGE: "en-US",

  // A shade under normal. These are single words heard once, often ones the
  // listener has never met, and the default rate clips the end of a long one.
  RATE: 0.9,

  voices: [],

  // Called once at load. The list is a snapshot: a device that reports its
  // voices late fires voiceschanged, and a device that never does simply
  // leaves this empty, which speak() treats as "use the default voice".
  warm() {
    if ( !this.supported() ) return;
    const self = this;
    const read = function () {
      try {
        self.voices = window.speechSynthesis.getVoices() || [];
      } catch ( voicesError ) {
        self.voices = [];
      }
    };
    read();
    window.speechSynthesis.addEventListener( "voiceschanged" , read );
  },

  supported() {
    return typeof window.speechSynthesis !== "undefined"
      && typeof window.SpeechSynthesisUtterance !== "undefined";
  },

  // Resolves once there is a voice list to show, or once it is clear there
  // will not be one. The training screen never needs this -- it speaks with
  // whatever is loaded and the platform default covers the gap -- but the
  // settings screen is listing the voices by name, and a list that fills in a
  // moment after the select is built is a select that is silently wrong.
  //
  // The timeout is the whole point: on a device that reports no voices at all
  // the event never fires, and waiting for it would hang the screen.
  READY_TIMEOUT: 1200,

  ready() {
    const self = this;
    return new Promise( function ( resolve ) {
      if ( !self.supported() || self.voices.length > 0 ) { resolve( self.voices ); return; }
      let settled = false;
      const finish = function () {
        if ( settled ) return;
        settled = true;
        window.speechSynthesis.removeEventListener( "voiceschanged" , finish );
        resolve( self.voices );
      };
      window.speechSynthesis.addEventListener( "voiceschanged" , finish );
      window.setTimeout( finish , self.READY_TIMEOUT );
    } );
  },

  // The voices worth offering. English only: the corpus is an English word
  // list whatever language the interface has been set to, and a device can
  // carry two hundred voices of which all but a few dozen would mispronounce
  // every card. Sorted by name so the list does not reshuffle between visits.
  choices() {
    return ( this.voices || [] )
      .filter( function ( voice ) { return String( voice.lang || "" ).toLowerCase().indexOf( "en" ) === 0; } )
      .slice()
      .sort( function ( a , b ) { return String( a.name ).localeCompare( String( b.name ) ); } );
  },

  // The name the user picked, if they have picked one. Read from the shell
  // rather than held here, so there is one copy of the settings and this file
  // has nothing to keep in step.
  preferredName() {
    return ( Shell.settings && Shell.settings.speech_voice ) || "";
  },

  // Which voice to speak with. The name the user chose wins if this device
  // has it; then an exact language match; then any English at all; then
  // nothing -- and nothing is a perfectly good answer, because leaving
  // utterance.voice unset asks the platform for its default, which is usually
  // the right one anyway.
  //
  // A chosen name that this device does not have falls through the same way.
  // That is deliberate rather than an error: the setting follows the user to
  // a phone whose voices are its own, and the honest response is to read the
  // word in some voice rather than to refuse.
  voice( name ) {
    const list = this.voices || [];
    const wanted = String( name === undefined ? this.preferredName() : name || "" );
    if ( wanted !== "" ) {
      const chosen = list.find( function ( voice ) { return voice.name === wanted; } );
      if ( chosen ) return chosen;
    }

    const language = this.LANGUAGE.toLowerCase();
    const matching = function ( test ) {
      return list.find( function ( voice ) { return test( String( voice.lang || "" ).toLowerCase() ); } );
    };
    return matching( function ( lang ) { return lang.replace( "_" , "-" ) === language; } )
      || matching( function ( lang ) { return lang.indexOf( "en" ) === 0; } )
      || null;
  },

  // speak is deliberately synchronous from the caller's point of view: no
  // promise, no await, nothing between the click and the queued utterance.
  // Anything else and iOS decides the speech did not come from a gesture and
  // silently drops it.
  // options: { voiceName, onFinish }. voiceName is for the settings screen,
  // which has to preview a choice that has not been saved yet; leave it out
  // and the saved one is used.
  speak( text , options ) {
    if ( !this.supported() ) return;
    const words = String( text || "" ).trim();
    if ( words === "" ) return;

    const settings = options || {};
    let finished = false;
    const done = function () {
      // end and error both fire on some engines, and cancel() below can
      // deliver a second one for an utterance already finished with.
      if ( finished ) return;
      finished = true;
      if ( settings.onFinish ) settings.onFinish();
    };

    try {
      // Pressing the button twice should re-read the word, not queue it
      // twice, so anything still going is cut off first. Only when there is
      // something to cut off: a cancel() on an idle engine is the documented
      // way to wedge Safari's queue, and the next speak() is then dropped
      // with no error and no sound.
      if ( window.speechSynthesis.speaking || window.speechSynthesis.pending ) {
        window.speechSynthesis.cancel();
      }

      const utterance = new window.SpeechSynthesisUtterance( words );
      utterance.lang = this.LANGUAGE;
      utterance.rate = this.RATE;
      const voice = this.voice( settings.voiceName );
      if ( voice ) {
        utterance.voice = voice;
        // Say it in the voice's own language rather than the one asked for.
        // An en-GB voice handed lang="en-US" is a mismatch some engines
        // resolve by quietly swapping the voice back out again.
        utterance.lang = voice.lang || this.LANGUAGE;
      }
      utterance.addEventListener( "end" , done );
      utterance.addEventListener( "error" , done );
      window.speechSynthesis.speak( utterance );
    } catch ( speakError ) {
      // A device that has the API but no working engine. Nothing to report:
      // the word is on the screen, which is the primary way to read it.
      done();
    }
  },

  // The speaker glyph, drawn rather than typed.
  //
  // Everywhere else in this app an icon is a text character, because a font
  // would be a dependency and the CSP would have to allow it. There is no
  // character for "speaker" outside the emoji block, and an emoji next to
  // display type on a card looks like something that fell in. An inline SVG
  // is neither a font nor a request -- it inherits currentColor, so it is
  // correct in both themes with no second copy.
  //
  // Built with createElementNS rather than through Dom.el because SVG lives
  // in its own namespace, and an <svg> made by createElement is an unknown
  // HTML element that renders as nothing at all.
  icon() {
    const namespace = "http://www.w3.org/2000/svg";
    const svg = document.createElementNS( namespace , "svg" );
    svg.setAttribute( "viewBox" , "0 0 24 24" );
    svg.setAttribute( "aria-hidden" , "true" );
    svg.setAttribute( "focusable" , "false" );

    const cone = document.createElementNS( namespace , "path" );
    cone.setAttribute( "d" , "M3 9.6a1 1 0 0 1 1-1h3.2l4.6-3.9a.7.7 0 0 1 1.2.6v13.4a.7.7 0 0 1-1.2.6l-4.6-3.9H4a1 1 0 0 1-1-1z" );
    cone.setAttribute( "fill" , "currentColor" );
    svg.appendChild( cone );

    [ "M16.2 9.2a4 4 0 0 1 0 5.6" , "M18.9 6.4a7.9 7.9 0 0 1 0 11.2" ].forEach( function ( d ) {
      const wave = document.createElementNS( namespace , "path" );
      wave.setAttribute( "d" , d );
      wave.setAttribute( "fill" , "none" );
      wave.setAttribute( "stroke" , "currentColor" );
      wave.setAttribute( "stroke-width" , "1.7" );
      wave.setAttribute( "stroke-linecap" , "round" );
      svg.appendChild( wave );
    } );

    return svg;
  },

  // A button that reads one word aloud, or null if there is no reason to
  // build one -- no speech engine on this device, or the label removed from
  // language.yaml, which is how every other piece of the UI is turned off.
  button( word ) {
    if ( !this.supported() ) return null;
    const label = I18n.get( "train.speak_label" );
    if ( label === "" ) return null;

    const self = this;
    const button = Dom.el( "button" , {
      class: "card-speak",
      attrs: { type: "button" , "aria-label": label , title: label },
    } );
    button.appendChild( this.icon() );

    // The card this button sits on is a swipe surface: the pointer stream is
    // captured on pointerdown and a short one is read as the tap that turns
    // the card over. Stopping the event here is what keeps pressing the
    // button from also answering or revealing the card -- without it, every
    // press to hear a word would flip it.
    button.addEventListener( "pointerdown" , function ( event ) { event.stopPropagation(); } );

    button.addEventListener( "click" , function ( event ) {
      event.stopPropagation();
      event.preventDefault();
      button.classList.add( "is-speaking" );
      self.speak( word , { onFinish: function () { button.classList.remove( "is-speaking" ); } } );
    } );

    return button;
  },
};

Speech.warm();
