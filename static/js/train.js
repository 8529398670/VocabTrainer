// The training screen: a stack of cards, three ways to answer each one.
//
// The interaction the rest of this file exists to serve:
//
//   "I already know it"  )  one swipe direction each, and which direction is
//   "I don't know it"    )  which is the user's choice -- see the swipe_*
//   skip, filed for later)  settings. The fourth direction does nothing.
//
// Answering reveals the hidden side before the card leaves, so a wrong guess
// is corrected immediately -- that reveal is most of the value of the
// exercise. How long it stays up (including not at all) is a setting too.
// Skipping never reveals anything: the point of a skip is that the user does
// not want to deal with the word at all right now.
//
// Note on CSP: this page may not use inline style attributes, so positions
// are written through element.style.transform (the CSSOM property, which CSP
// does not gate) and never with setAttribute("style", ...), which it does.
const Trainer = {
  queue: [],
  at: 0,
  settings: null,

  revealed: false,
  // Set once an outcome has been recorded and the answer is on screen. The
  // card is spent at that point -- any further input just moves things along.
  awaitingContinue: false,
  pendingDirection: null,
  advanceTimer: null,
  busy: false,

  // direction -> outcome. Rebuilt from the settings on every deck load, and
  // the only copy of the mapping: swipes, buttons and keys all read it, so
  // the three cannot drift apart.
  outcomeFor: {},

  arrows: { left: "←" , right: "→" , up: "↑" , down: "↓" },

  // Where each direction's button sits in the row, left to right. Anything
  // vertical goes in the middle, which is the only place it can go once the
  // horizontal ones have claimed the ends.
  rowOrder: { left: 0 , up: 1 , down: 2 , right: 3 },

  // What the defaults are, for a settings record too old to carry the fields.
  // The server normalises this too; this is the belt to its braces.
  fallbackDirections: { unknown: "left" , skip: "up" , known: "right" },

  async init() {
    this.settings = await Shell.loadSettings();
    this.applyDirections();
    this.bindButtons();
    this.bindKeyboard();
    this.bindResync();
    await this.refill();
    this.renderControls();
    this.render();
  },

  current() { return this.queue[ this.at ] || null; },
  next()    { return this.queue[ this.at + 1 ] || null; },

  // The settings store one direction per outcome, because that is the
  // question the settings screen asks. Every input arrives as a direction, so
  // the mapping is inverted once here rather than searched on each swipe.
  applyDirections() {
    const settings = this.settings || {};
    const fallback = this.fallbackDirections;
    this.outcomeFor = {};
    this.outcomeFor[ settings.swipe_unknown || fallback.unknown ] = "unknown";
    this.outcomeFor[ settings.swipe_skip    || fallback.skip    ] = "skip";
    this.outcomeFor[ settings.swipe_known   || fallback.known   ] = "known";
  },

  directionOf( outcome ) {
    const self = this;
    return Object.keys( this.outcomeFor ).find( function ( direction ) {
      return self.outcomeFor[ direction ] === outcome;
    } ) || null;
  },

  // How long the answer stays up when a card is answered before it was
  // revealed. Zero means it is not shown at all, and a negative value means
  // it stays until the next answer -- see RevealHoldMs in the settings model.
  revealHold() {
    const value = this.settings ? Number( this.settings.reveal_hold_ms ) : NaN;
    return Number.isFinite( value ) ? value : 3000;
  },

  practising() {
    return !!this.settings && this.settings.practice_mode === "unknown_only";
  },

  async refill() {
    const payload = await Api.deck();
    if ( payload.settings ) {
      this.settings = payload.settings;
      Shell.settings = payload.settings;
      this.applyDirections();
    }
    this.queue = payload.cards || [];
    this.at = 0;
    this.updateCounts( payload.counts );
  },

  updateCounts( counts ) {
    if ( !counts ) return;
    const label = Dom.get( "deck-counts" );
    if ( !label ) return;
    Dom.text( label , counts.known + " / " + counts.total );
  },

  // --- rendering ---------------------------------------------------------

  render() {
    const stack = Dom.get( "card-stack" );
    if ( !stack ) return;
    Dom.clear( stack );

    this.renderPracticeBadge();

    const card = this.current();
    if ( !card ) {
      this.renderEmpty();
      Dom.show( Dom.get( "deck-empty" ) , true );
      Dom.show( Dom.get( "deck-controls" ) , false );
      Dom.show( Dom.get( "deck-meta" ) , false );
      return;
    }

    Dom.show( Dom.get( "deck-empty" ) , false );
    Dom.show( Dom.get( "deck-controls" ) , true );
    Dom.show( Dom.get( "deck-meta" ) , true );

    // The card behind is painted first so the live one sits on top: both are
    // absolutely positioned, so DOM order is what decides.
    const behind = this.next();
    if ( behind ) stack.appendChild( this.buildCard( behind , false , true ) );

    const top = this.buildCard( card , this.revealed , false );
    stack.appendChild( top );
    this.attachGestures( top );
    this.markScrollable( top );

    this.renderProgress();
  },

  // Running out of cards means something different when the deck is the
  // user's own not-known list: there is nothing to wait for, and the way out
  // is a setting rather than patience.
  renderEmpty() {
    const practising = this.practising();
    const write = function ( id , key ) {
      const element = Dom.get( id );
      if ( !element ) return;
      const text = I18n.get( key );
      Dom.text( element , text );
      Dom.show( element , text !== "" );
    };
    write( "deck-empty-heading" , practising ? "train.empty_practice_heading" : "train.empty_heading" );
    write( "deck-empty-body" , practising ? "train.empty_practice_body" : "train.empty_body" );
  },

  renderPracticeBadge() {
    const badge = Dom.get( "deck-practice" );
    if ( !badge ) return;
    const text = I18n.get( "train.practice_badge" );
    Dom.text( badge , text );
    Dom.show( badge , this.practising() && text !== "" );
  },

  // The buttons carry the direction each answer is bound to -- both the arrow
  // on the button and where the button sits in the row. Moving them matters
  // as much as relabelling them: a button marked "left" sitting on the right
  // of the row is a worse picture of the mapping than no picture at all.
  //
  // The nodes themselves are reordered rather than their CSS order, so the
  // keyboard tab order moves with the layout instead of diverging from it.
  renderControls() {
    const self = this;
    const row = Dom.get( "deck-actions" );
    const buttons = [ [ "action-unknown" , "unknown" ] , [ "action-skip" , "skip" ] , [ "action-known" , "known" ] ]
      .map( function ( pair ) {
        const button = Dom.get( pair[ 0 ] );
        const direction = self.directionOf( pair[ 1 ] );
        if ( button && direction ) {
          const icon = button.querySelector( ".action-icon" );
          if ( icon ) Dom.text( icon , self.arrows[ direction ] );
        }
        return { button: button , direction: direction };
      } )
      .filter( function ( entry ) { return entry.button && entry.direction; } );

    if ( row ) {
      buttons
        .slice()
        .sort( function ( a , b ) { return self.rowOrder[ a.direction ] - self.rowOrder[ b.direction ]; } )
        // appendChild on a node that is already in the row moves it, and a
        // moved node keeps the click handler bound to it.
        .forEach( function ( entry ) { row.appendChild( entry.button ); } );
    }

    const help = Dom.get( "gesture-help" );
    if ( !help ) return;
    const named = function ( outcome ) {
      return I18n.get( "train.direction_" + self.directionOf( outcome ) );
    };
    const text = I18n.format( "train.gesture_help" , {
      known: named( "known" ) , unknown: named( "unknown" ) , skip: named( "skip" ),
    } );
    Dom.text( help , text );
    Dom.show( help , text !== "" );
  },

  renderProgress() {
    const bar = Dom.get( "deck-progress-fill" );
    if ( !bar ) return;
    const total = this.queue.length || 1;
    bar.style.width = Math.round( ( this.at / total ) * 100 ) + "%";

    const remaining = Dom.get( "deck-remaining" );
    if ( remaining ) {
      Dom.text( remaining , ( this.queue.length - this.at ) + " " + I18n.get( "train.remaining" ) );
    }
  },

  // A card is three bands: the top holds the badges and the hints for every
  // direction but down, the face holds the word, and the foot exists only to
  // give a downward hint somewhere of its own. The bands are what keep a hint
  // from ever being painted over the text underneath it.
  buildCard( card , revealed , isBehind ) {
    const node = Dom.el( "article" , { class: "swipe-card" + ( isBehind ? " is-behind" : "" ) } );
    node.appendChild( this.buildTop( card ) );
    node.appendChild( this.buildFace( card , revealed ) );
    const foot = this.buildFoot();
    if ( foot ) node.appendChild( foot );
    return node;
  },

  buildTop( card ) {
    const top = Dom.el( "div" , { class: "card-top" } );
    top.appendChild( this.buildBadges( card ) );
    const self = this;
    [ "left" , "right" , "up" ].forEach( function ( direction ) {
      const hint = self.buildHint( direction );
      if ( hint ) top.appendChild( hint );
    } );
    return top;
  },

  buildFoot() {
    const hint = this.buildHint( "down" );
    if ( !hint ) return null;
    const foot = Dom.el( "div" , { class: "card-foot" } );
    foot.appendChild( hint );
    return foot;
  },

  // A hint exists only for a direction that is bound to something, so an
  // unused direction has nothing to light up.
  buildHint( direction ) {
    const outcome = this.outcomeFor[ direction ];
    if ( !outcome ) return null;
    return Dom.el( "div" , {
      class: "swipe-hint swipe-hint-" + outcome,
      text: I18n.get( "train.hint_" + outcome ),
      attrs: { "data-direction": direction },
    } );
  },

  buildBadges( card ) {
    const badges = Dom.el( "div" , { class: "card-badges" } );
    badges.appendChild( Dom.el( "span" , {
      class: "badge " + ( card.is_new ? "badge-new" : "badge-review" ),
      text: I18n.get( card.is_new ? "train.new_badge" : "train.review_badge" ),
    } ) );
    badges.appendChild( Dom.el( "span" , { class: "badge" , text: Shell.levelName( card.tier ) } ) );
    return badges;
  },

  // buildFace decides which half of the card is face down. That is the whole
  // of the "card direction" setting: one side is always shown, the other
  // waits for a tap.
  //
  // The prompt goes in a head that never scrolls and the answer in a body
  // that does. A single scrolling box centred on its contents pushes the
  // first line out through the top of the card, where it cannot be scrolled
  // back into view -- which is how the word itself used to disappear behind
  // the badges on a card with several senses.
  buildFace( card , revealed ) {
    const face = Dom.el( "div" , { class: "card-face" } );
    const senses = card.senses || [];
    const primary = senses[ 0 ] || { definition: "" , pos: "" };
    const hideWord = this.settings.reveal_mode === "word_hidden";

    const head = Dom.el( "div" , { class: "card-head" } );
    const body = Dom.el( "div" , { class: "card-body" } );

    if ( hideWord ) {
      head.appendChild( Dom.el( "p" , { class: "card-pos" , text: primary.pos } ) );
      head.appendChild( Dom.el( "p" , { class: "card-prompt-definition" , text: primary.definition } ) );
      if ( revealed ) {
        body.appendChild( Dom.el( "div" , { class: "card-divider" } ) );
        body.appendChild( Dom.el( "p" , { class: "card-word" , text: card.word } ) );
        if ( senses.length > 1 ) body.appendChild( this.buildSenses( senses.slice( 1 ) ) );
        this.appendExample( body , primary );
      }
    } else {
      head.appendChild( Dom.el( "p" , { class: "card-word" , text: card.word } ) );
      if ( revealed ) {
        body.appendChild( Dom.el( "div" , { class: "card-divider" } ) );
        body.appendChild( this.buildSenses( senses ) );
      }
    }

    face.appendChild( head );
    face.appendChild( body );

    const hint = this.buildFaceHint( revealed );
    if ( hint ) face.appendChild( hint );
    return face;
  },

  // The pill at the bottom of the face: what the next tap will do. It sits
  // outside the scrolling half deliberately -- an instruction that has
  // scrolled out of sight is not an instruction.
  buildFaceHint( revealed ) {
    let key = "train.tap_to_reveal";
    if ( revealed ) {
      // A card the user turned over themselves needs no instruction, and one
      // on a timer is about to leave anyway. Only a card waiting indefinitely
      // has something to say.
      if ( this.awaitingContinue === false || this.revealHold() >= 0 ) return null;
      key = "train.tap_to_continue";
    }
    const text = I18n.get( key );
    if ( text === "" ) return null;
    return Dom.el( "p" , { class: "card-hidden-hint" , text: text } );
  },

  buildSenses( senses ) {
    const list = Dom.el( "div" , { class: "card-senses" } );
    const self = this;
    senses.forEach( function ( sense ) {
      const block = Dom.el( "div" , { class: "card-sense" } );
      if ( sense.pos ) block.appendChild( Dom.el( "p" , { class: "card-pos" , text: sense.pos } ) );
      block.appendChild( Dom.el( "p" , { class: "card-sense-definition" , text: sense.definition } ) );
      self.appendExample( block , sense );
      list.appendChild( block );
    } );
    return list;
  },

  appendExample( into , sense ) {
    if ( !this.settings.show_examples || !sense.example ) return;
    into.appendChild( Dom.el( "p" , { class: "card-sense-example" , text: "“" + sense.example + "”" } ) );
  },

  // A card gives the browser its vertical gestures back only where the text
  // genuinely does not fit. The card sets touch-action: none so a swipe is
  // never stolen by the page; lifting that on a body that fits would cost a
  // swipe direction to enable scrolling with nothing to scroll.
  markScrollable( node ) {
    const body = node.querySelector( ".card-body" );
    if ( !body ) return;
    body.classList.toggle( "is-scrollable" , body.scrollHeight > body.clientHeight + 1 );
  },

  // --- gestures ----------------------------------------------------------

  attachGestures( node ) {
    const self = this;
    Swipe.attach( node , {
      allows( direction ) { return !!self.outcomeFor[ direction ]; },
      onMove( dx , dy ) { self.followFinger( node , dx , dy ); },
      onCancel() { self.settle( node ); },
      onTap() { self.handleTap(); },
      onCommit( direction ) { self.handleOutcome( direction , node ); },
    } );
  },

  followFinger( node , dx , dy ) {
    node.classList.remove( "is-animating" );
    // A small rotation tied to horizontal travel is what makes the card feel
    // like a physical object rather than a sliding rectangle.
    const tilt = dx / 18;
    node.style.transform = "translate(" + dx + "px, " + dy + "px) rotate(" + tilt + "deg)";

    const progress = function ( value ) {
      return Math.max( 0 , Math.min( 1 , Math.abs( value ) / Swipe.DISTANCE ) );
    };
    const strength = {
      right: dx > 0 ? progress( dx ) : 0,
      left:  dx < 0 ? progress( dx ) : 0,
      up:    dy < 0 ? progress( dy ) : 0,
      down:  dy > 0 ? progress( dy ) : 0,
    };

    const self = this;
    let loudest = 0;
    Object.keys( strength ).forEach( function ( direction ) {
      self.setHint( node , direction , self.hintOpacity( strength[ direction ] ) );
      if ( strength[ direction ] > loudest ) loudest = strength[ direction ];
    } );
    // The badges share the top band with three of the hints, so the band is
    // handed from one to the other rather than shared: the badges have faded
    // out by the point the first hint fades in, and the two are never drawn
    // over each other at any distance.
    this.setBadgeOpacity( node , Math.max( 0 , 1 - loudest / this.HANDOVER ) );
  },

  // How far into a drag the top band changes hands. Below this the badges are
  // still readable and no hint is drawn; above it the hint has the band to
  // itself and grows to full strength at the distance that commits.
  HANDOVER: 0.35,

  hintOpacity( progress ) {
    if ( progress <= this.HANDOVER ) return 0;
    return ( progress - this.HANDOVER ) / ( 1 - this.HANDOVER );
  },

  setHint( node , direction , opacity ) {
    const hint = node.querySelector( '.swipe-hint[data-direction="' + direction + '"]' );
    if ( hint ) hint.style.opacity = String( opacity );
  },

  setBadgeOpacity( node , opacity ) {
    const badges = node.querySelector( ".card-badges" );
    if ( badges ) badges.style.opacity = String( opacity );
  },

  settle( node ) {
    node.classList.add( "is-animating" );
    node.style.transform = "";
    const self = this;
    [ "left" , "right" , "up" , "down" ].forEach( function ( direction ) {
      self.setHint( node , direction , 0 );
    } );
    this.setBadgeOpacity( node , 1 );
  },

  flyAway( node , direction ) {
    node.classList.add( "is-animating" , "is-gone" );
    const offsets = {
      right: "translate(120vw, 0) rotate(22deg)",
      left:  "translate(-120vw, 0) rotate(-22deg)",
      up:    "translate(0, -110vh) rotate(0deg)",
      down:  "translate(0, 110vh) rotate(0deg)",
    };
    node.style.transform = offsets[ direction ] || offsets.up;
  },

  handleTap() {
    if ( this.awaitingContinue ) {
      this.advance();
      return;
    }
    if ( this.revealed || this.busy ) return;
    this.revealed = true;
    this.render();
  },

  // handleOutcome is the one place a swipe, a button press and a key press
  // all end up, so the three input methods cannot drift apart.
  async handleOutcome( direction , node ) {
    // Once the answer is showing, the card has already been recorded. Any
    // further input means "move on" rather than a second opinion.
    if ( this.awaitingContinue ) {
      this.advance();
      return;
    }
    if ( this.busy ) return;

    const card = this.current();
    if ( !card ) return;
    const outcome = this.outcomeFor[ direction ];
    if ( !outcome ) return;

    this.busy = true;
    Shell.buzz( outcome === "unknown" ? 22 : 12 );

    const top = node || Dom.get( "card-stack" ).lastElementChild;

    // The write is started now but not waited for: the animation should not
    // stutter on the network. A failure surfaces in the banner, and the card
    // is recoverable from the word lists either way.
    const self = this;
    Api.review( card.word , outcome ).then( function ( payload ) {
      self.updateCounts( payload.counts );
    } ).catch( function ( error ) {
      Shell.showError( error );
    } );

    const hold = this.revealHold();

    // Three ways a card leaves immediately: a skip, which never reveals
    // because setting a word aside means not engaging with it at all; a card
    // already turned over, whose answer has been read; and a hold of zero,
    // which is the user saying they would rather not be shown the answer they
    // did not ask for.
    if ( outcome === "skip" || this.revealed || hold === 0 ) {
      if ( top ) this.flyAway( top , direction );
      window.setTimeout( function () { self.advance(); } , 180 );
      return;
    }

    // Not yet revealed: bring the card back to the middle and turn it over,
    // so the answer is read before it leaves.
    this.revealed = true;
    this.awaitingContinue = true;
    this.pendingDirection = direction;
    if ( top ) this.settle( top );
    this.render();

    // A negative hold has no timer: the card waits for the next answer, for
    // as long as that takes.
    if ( hold > 0 ) {
      this.advanceTimer = window.setTimeout( function () { self.advance(); } , hold );
    }
  },

  async advance() {
    window.clearTimeout( this.advanceTimer );
    this.advanceTimer = null;

    const direction = this.pendingDirection;
    const stack = Dom.get( "card-stack" );
    if ( direction && stack && stack.lastElementChild ) {
      this.flyAway( stack.lastElementChild , direction );
    }

    this.at += 1;
    this.revealed = false;
    this.awaitingContinue = false;
    this.pendingDirection = null;
    this.busy = false;

    if ( this.at >= this.queue.length ) {
      try {
        await this.refill();
        this.renderControls();
      } catch ( refillError ) {
        Shell.showError( refillError );
        this.queue = [];
      }
    }
    this.render();
  },

  // --- buttons and keys --------------------------------------------------

  bindButtons() {
    const self = this;
    // Bound by outcome, not by direction: the direction is looked up when the
    // button is pressed, so changing the mapping never leaves a stale button.
    const wire = function ( id , outcome ) {
      const button = Dom.get( id );
      if ( !button ) return;
      button.addEventListener( "click" , function () {
        const stack = Dom.get( "card-stack" );
        self.handleOutcome( self.directionOf( outcome ) , stack ? stack.lastElementChild : null );
      } );
    };
    wire( "action-unknown" , "unknown" );
    wire( "action-skip" , "skip" );
    wire( "action-known" , "known" );

    const reveal = Dom.get( "action-reveal" );
    if ( reveal ) reveal.addEventListener( "click" , function () { self.handleTap(); } );
  },

  // A page restored from the back/forward cache never runs DOMContentLoaded
  // again, so on its own it would keep showing whatever mapping was current
  // when it was last open. That is exactly the page someone lands on after
  // changing the directions and pressing Back, so it re-reads the settings
  // instead of trusting the copy it loaded with.
  bindResync() {
    const self = this;
    window.addEventListener( "pageshow" , function ( event ) {
      if ( event.persisted ) self.resync();
    } );
    document.addEventListener( "visibilitychange" , function () {
      if ( document.visibilityState === "visible" ) self.resync();
    } );
  },

  async resync() {
    // Never cut in on a card that is part-way through being answered.
    if ( this.busy || this.awaitingContinue ) return;

    let payload = null;
    try {
      payload = await Api.getSettings();
    } catch ( error ) {
      // Offline, or the session has gone. The screen keeps working with what
      // it already has; this is a refresh, not a requirement.
      return;
    }
    if ( !payload || !payload.settings ) return;
    if ( JSON.stringify( payload.settings ) === JSON.stringify( this.settings ) ) return;

    this.settings = payload.settings;
    Shell.settings = payload.settings;
    this.applyDirections();
    this.renderControls();

    // The settings changed while this page was away, so the deck it is
    // holding may have been built under the old ones -- a run of new words
    // when the user has since asked to practise only what they do not know.
    try {
      await this.refill();
      this.renderControls();
    } catch ( refillError ) {
      Shell.showError( refillError );
    }
    this.render();
  },

  bindKeyboard() {
    const self = this;
    const keys = {
      ArrowLeft: "left" , ArrowRight: "right" , ArrowUp: "up" , ArrowDown: "down",
    };
    document.addEventListener( "keydown" , function ( event ) {
      // Never steal keys from a text field -- the search box on this page's
      // siblings would become unusable.
      const tag = event.target && event.target.tagName;
      if ( tag === "INPUT" || tag === "TEXTAREA" || tag === "SELECT" ) return;

      const stack = Dom.get( "card-stack" );
      const top = stack ? stack.lastElementChild : null;

      // Each arrow key matches the swipe it points at, so the keyboard
      // follows the same mapping as the thumb -- including the arrow for the
      // unused direction, which is left to the browser rather than swallowed,
      // exactly as a swipe that way is.
      const direction = keys[ event.key ];
      if ( direction && self.outcomeFor[ direction ] ) {
        event.preventDefault();
        self.handleOutcome( direction , top );
        return;
      }
      if ( event.key === " " || event.key === "Enter" ) {
        event.preventDefault();
        self.handleTap();
      }
    } );
  },
};

document.addEventListener( "DOMContentLoaded" , function () {
  Shell.boot( "/" , function () { return Trainer.init(); } );
} );
