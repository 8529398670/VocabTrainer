// The training screen: a stack of cards, and either three or five ways to
// answer each one.
//
// The interaction the rest of this file exists to serve:
//
//   "I already know it"  )  one swipe direction each, and which direction is
//   "I don't know it"    )  which is the user's choice -- see the swipe_*
//   skip, filed for later)  settings. The fourth direction does nothing.
//
// With the four-grade scoring mode on (settings.scoring_mode), the same three
// gestures answer Again, Easy and skip -- the two extremes of the grade
// scale, plus the skip that is not on it -- and Hard and Good join them on
// the buttons and on the number keys. The two extremes are what a swipe is
// good for: they are the two answers you never have to aim for, and the
// middle two are exactly the ones worth a deliberate press.
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
  // The exit the recorded card is waiting to make. pendingDirection is null
  // for an answer that has no direction on it -- Hard and Good -- so the
  // flag is what says a card is waiting to leave at all.
  pendingExit: false,
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

  // The four grades, soonest first, which is the order the row is drawn in
  // and the order the number keys follow. Two of them are outcomes the app
  // already had under other names: Again is "unknown" and Good is "known" --
  // see the note above OutcomeHard in server/models/progress.go.
  gradeButtons: [
    { id: "grade-again" , outcome: "unknown" },
    { id: "grade-hard"  , outcome: "hard" },
    { id: "grade-good"  , outcome: "known" },
    { id: "grade-easy"  , outcome: "easy" },
  ],

  async init() {
    this.settings = await Shell.loadSettings();
    this.applyDirections();
    this.bindButtons();
    this.bindKeyboard();
    this.bindResync();
    this.bindRefit();
    await this.refill();
    this.renderControls();
    this.render();
  },

  current() { return this.queue[ this.at ] || null; },
  next()    { return this.queue[ this.at + 1 ] || null; },

  // Whether the card offers four grades rather than two answers.
  graded() { return !!this.settings && this.settings.scoring_mode === "graded"; },

  // The settings store one direction per outcome, because that is the
  // question the settings screen asks. Every input arrives as a direction, so
  // the mapping is inverted once here rather than searched on each swipe.
  applyDirections() {
    const settings = this.settings || {};
    const fallback = this.fallbackDirections;
    this.outcomeFor = {};

    if ( this.graded() ) {
      // The two horizontal answers are fixed here rather than read from the
      // settings, which is the one place the grades overrule a preference.
      //
      // The four grades are a scale, drawn left to right from soonest to
      // latest, and the arrows on them have to agree with that: Again sits at
      // the left end, so it is the leftward swipe, and Easy sits at the right
      // end, so it is the rightward one. Honouring a mapping that pointed
      // them the other way would put a rightward arrow on the leftmost button
      // -- a row contradicting its own order, which is worse than a row with
      // no arrows at all.
      this.outcomeFor.left = "unknown";
      this.outcomeFor.right = "easy";
      // Skip is still the user's to place, but only on the axis the grades
      // have not claimed. Anything horizontal falls back to up.
      this.outcomeFor[ settings.swipe_skip === "down" ? "down" : "up" ] = "skip";
      return;
    }

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

  // Whether this answer is allowed to leave without the forced reveal.
  //
  // The reveal exists to correct a wrong guess on the spot, and there is no
  // wrong guess to correct when the answer was "I already know this" -- so
  // someone clearing familiar words is only being held up by it. Off by
  // default: the reveal is the point of the exercise for every other answer,
  // and this is a deliberate opt-out.
  excusedFromReveal( outcome ) {
    if ( !this.settings || !this.settings.known_skips_reveal ) return false;
    // Good and Easy are both the answer this switch is about: the word was
    // known, and there is nothing to correct. Hard is not -- it means the
    // word came back slowly, which is precisely when seeing it again is
    // worth the second.
    return outcome === "known" || outcome === "easy";
  },

  // How long the phone buzzes for each answer. A miss is the one worth
  // feeling, and with the grades on there is a middle to feel too.
  buzzFor: { unknown: 22 , hard: 16 },

  busyBuzz( outcome ) {
    Shell.buzz( this.buzzFor[ outcome ] || 12 );
  },

  practising() {
    return !!this.settings && this.settings.practice_mode === "unknown_only";
  },

  // The mode with no scheduler in it: new words, one batch after another,
  // with no end to reach. Nothing here treats a run as a unit, so the two
  // things that count one -- the progress bar and the cards-left figure --
  // are the two things that go.
  endless() {
    return !!this.settings && this.settings.practice_mode === "new_only";
  },

  // extra, when given, is a number of new words to add on top of whatever the
  // deck would normally hold, ignoring the daily limit. Only loadMore() passes
  // it; a normal refill leaves the pacing alone.
  async refill( extra ) {
    const payload = await Api.deck( extra );
    if ( payload.settings ) {
      this.settings = payload.settings;
      Shell.settings = payload.settings;
      this.applyDirections();
    }
    this.queue = payload.cards || [];
    this.at = 0;
    if ( this.queue.length > 0 ) this.exhausted = false;
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
      // The stack has to go, not just be emptied. It shares its grid cells
      // with the empty state and is position: relative, which puts it above
      // an unpositioned sibling however the two are ordered in the markup --
      // so an empty stack left in place is an invisible sheet over the empty
      // screen, and every button and link on it stops answering.
      Dom.show( stack , false );
      return;
    }

    Dom.show( stack , true );
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

    // Both cards, not just the live one: the card behind is visible through
    // the gap around the top card, and a word wrapped there gives the trick
    // away as surely as one wrapped in front.
    const self = this;
    Dom.all( ".swipe-card" , stack ).forEach( function ( node ) { self.fitCard( node ); } );

    if ( this.graded() ) this.renderGradeIntervals();
    this.renderProgress();
  },

  // Sizing a card is done after it is in the document, because every number
  // it depends on -- how wide the card ended up, how much height the answer
  // left for the prompt -- only exists once the layout has run.
  fitCard( node ) {
    const head = node.querySelector( ".card-head" );
    const body = node.querySelector( ".card-body" );

    // The word first and on its own terms: it is the one piece of text that
    // should never wrap, so it is given the width before anything else asks
    // for height.
    Dom.all( ".card-word" , node ).forEach( function ( word ) { Fit.toOneLine( word ); } );

    // Then the prompt, against whatever height the head actually has. A
    // definition can wrap freely -- it is a sentence -- so this only stops it
    // needing a scrollbar for the sake of a line or two.
    const prompt = node.querySelector( ".card-prompt-definition" );
    if ( prompt && head ) Fit.toContainer( prompt , head , 0.7 );

    // Width was the interesting half of fitting the word, but not the only
    // half: a phone on its side leaves a card barely taller than one line of
    // display type, and a word that fits across but not down is clipped at
    // the ascenders. Shrinking it again here costs nothing when there was
    // room, because the loop exits on its first test.
    const headWord = head ? head.querySelector( ".card-word" ) : null;
    if ( headWord ) Fit.toContainer( headWord , head , 0.42 );

    Fit.markScroll( head );
    Fit.markScroll( body );
  },

  // Rotating the phone changes every measurement this screen depends on, and
  // so does the iOS toolbar sliding away under a scroll. Re-measuring on the
  // next frame rather than on the event itself means one pass per settled
  // size instead of one per pixel of an animated rotation.
  bindRefit() {
    const self = this;
    let pending = 0;
    const refit = function () {
      if ( pending ) window.cancelAnimationFrame( pending );
      pending = window.requestAnimationFrame( function () {
        pending = 0;
        const stack = Dom.get( "card-stack" );
        if ( !stack ) return;
        Dom.all( ".swipe-card" , stack ).forEach( function ( node ) { self.fitCard( node ); } );
      } );
    };
    window.addEventListener( "resize" , refit );
    window.addEventListener( "orientationchange" , refit );
    if ( window.visualViewport ) window.visualViewport.addEventListener( "resize" , refit );
  },

  // Running out of cards means something different when the deck is the
  // user's own not-known list: there is nothing to wait for, and the way out
  // is a setting rather than patience.
  renderEmpty() {
    const practising = this.practising();
    const write = function ( element , text ) {
      if ( !element ) return;
      Dom.text( element , text );
      Dom.show( element , text !== "" );
    };

    const endless = this.endless();

    let headingKey = "train.empty_heading";
    let bodyKey = "train.empty_body";
    if ( practising ) {
      headingKey = "train.empty_practice_heading";
      bodyKey = "train.empty_practice_body";
    } else if ( endless ) {
      // This deck has no daily limit and nothing to wait for, so the only
      // thing that can empty it is the reading level itself running dry --
      // which is what empty_more_none says.
      headingKey = "train.empty_new_heading";
      bodyKey = "train.empty_more_none";
    } else if ( this.exhausted ) {
      bodyKey = "train.empty_more_none";
    }

    write( Dom.get( "deck-empty-heading" ) , I18n.get( headingKey ) );
    write( Dom.get( "deck-empty-body" ) , I18n.get( bodyKey ) );

    // Nothing new to offer in any of the three cases where there is nothing
    // new to offer: the not-known deck draws no new words by design, the
    // endless deck has just failed to draw any, and an exhausted level has
    // none left at all.
    const more = Dom.get( "deck-more" );
    if ( more ) {
      const label = I18n.format( "train.empty_more" , { count: this.MORE_WORDS } );
      Dom.text( more , label );
      Dom.show( more , label !== "" && !practising && !endless && !this.exhausted );
      more.disabled = false;
    }
  },

  // How many new words the button on the empty screen asks for. The number
  // goes into the label through I18n.format rather than being written into
  // the wording, so the two cannot disagree.
  MORE_WORDS: 10,

  // Set when a request for more words came back with none. The level is spent
  // -- there is no point offering the button again, and "come back later" is
  // the wrong thing to say about a deck that will never refill on its own.
  exhausted: false,

  // The empty screen's way out: another handful of new words, past the daily
  // limit, without having to go and change a setting. The limit is there to
  // pace someone who has not asked; this is someone who has.
  async loadMore() {
    const button = Dom.get( "deck-more" );
    if ( button ) {
      if ( button.disabled ) return;
      button.disabled = true;
    }
    try {
      await this.refill( this.MORE_WORDS );
      this.exhausted = this.queue.length === 0;
      this.renderControls();
      this.render();
    } catch ( error ) {
      Shell.showError( error );
      if ( button ) button.disabled = false;
    }
  },

  // Says which deck this is whenever it is not the ordinary one. Both of the
  // narrowed decks need it for the same reason: a short run, or a run of
  // nothing but new words, is otherwise indistinguishable from the scheduler
  // behaving strangely.
  renderPracticeBadge() {
    const badge = Dom.get( "deck-practice" );
    if ( !badge ) return;
    let key = "";
    if ( this.practising() ) key = "train.practice_badge";
    else if ( this.endless() ) key = "train.endless_badge";
    const text = key === "" ? "" : I18n.get( key );
    Dom.text( badge , text );
    Dom.show( badge , text !== "" );
  },

  // Picks the row of answers this card gets, and draws it. Called when the
  // settings change rather than per card, because nothing in either row
  // depends on which word is showing -- the delays under the grades do, and
  // they are drawn separately, by renderGradeIntervals.
  renderControls() {
    const graded = this.graded();
    // One row or the other, never both -- they answer the same card and a
    // screen offering two sets of answers to one question is a screen nobody
    // trusts.
    Dom.show( Dom.get( "deck-actions" ) , !graded );
    Dom.show( Dom.get( "deck-grades" ) , graded );

    if ( graded ) this.renderGradeControls();
    else this.renderAnswerControls();

    this.renderGestureHelp();
  },

  // The three-answer row. The buttons carry the direction each answer is
  // bound to -- both the arrow on the button and where the button sits in the
  // row. Moving them matters as much as relabelling them: a button marked
  // "left" sitting on the right of the row is a worse picture of the mapping
  // than no picture at all.
  //
  // The nodes themselves are reordered rather than their CSS order, so the
  // keyboard tab order moves with the layout instead of diverging from it.
  renderAnswerControls() {
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

    if ( !row ) return;
    buttons
      .slice()
      .sort( function ( a , b ) { return self.rowOrder[ a.direction ] - self.rowOrder[ b.direction ]; } )
      // appendChild on a node that is already in the row moves it, and a
      // moved node keeps the click handler bound to it.
      .forEach( function ( entry ) { row.appendChild( entry.button ); } );
  },

  // The four-grade row, which is never reordered: the grades are in order of
  // how soon the word comes back, and that order is what the labels mean.
  //
  // Only the two ends take an arrow, and the middle two are deliberately left
  // bare. An arrow there would promise a gesture that does nothing, which is
  // a worse lie than saying nothing at all.
  renderGradeControls() {
    const self = this;
    const arrow = function ( button , outcome ) {
      if ( !button ) return;
      const icon = button.querySelector( ".action-icon" );
      if ( !icon ) return;
      const direction = self.directionOf( outcome );
      Dom.text( icon , direction ? self.arrows[ direction ] : "" );
      Dom.show( icon , !!direction );
    };

    this.gradeButtons.forEach( function ( entry ) {
      arrow( Dom.get( entry.id ) , entry.outcome );
    } );
  },

  // The line of prose under the buttons, and whether there is one.
  //
  // There is not, once the grades are on. Four buttons labelled with their
  // own names and their own delays have already said everything the sentence
  // would, and on a phone it was two lines of a screen that had none to
  // spare. The keyboard line stays, reworded: the number keys are the only
  // way to reach Hard and Good without a pointer, so they are worth saying
  // out loud.
  renderGestureHelp() {
    const graded = this.graded();
    const help = Dom.get( "gesture-help" );
    const self = this;
    // An outcome with no direction on it names nothing, which leaves the
    // placeholder showing rather than the word "undefined" -- see I18n.format.
    const named = function ( outcome ) {
      const direction = self.directionOf( outcome );
      return direction ? I18n.get( "train.direction_" + direction ) : "";
    };

    if ( help ) {
      const text = graded ? "" : I18n.format( "train.gesture_help" , {
        known: named( "known" ) , unknown: named( "unknown" ) , skip: named( "skip" ),
      } );
      Dom.text( help , text );
      Dom.show( help , text !== "" );
    }

    const keys = Dom.get( "keyboard-help" );
    if ( !keys ) return;
    const text = I18n.get( graded ? "train.keyboard_help_graded" : "train.keyboard_help" );
    Dom.text( keys , text );
    Dom.show( keys , text !== "" );
  },

  // The delay each grade would buy, printed under its label. This is the one
  // thing Anki's four buttons genuinely need: a grade whose consequence you
  // cannot see is a grade you are guessing at.
  //
  // It belongs to the card rather than to the settings, so it is redrawn with
  // each card instead of once when the controls are built.
  renderGradeIntervals() {
    const card = this.current();
    const intervals = ( card && card.intervals ) || {};
    const self = this;
    this.gradeButtons.forEach( function ( entry ) {
      const button = Dom.get( entry.id );
      const slot = button ? button.querySelector( ".grade-interval" ) : null;
      if ( !slot ) return;
      const text = self.formatInterval( intervals[ entry.outcome ] );
      Dom.text( slot , text );
      Dom.show( slot , text !== "" );
    } );
  },

  // Seconds into the shortest sensible unit: "10m", "4d", "3mo". The server
  // sends seconds because it is the only unit that needs no agreement; which
  // one to print is a question about reading, so it is answered here.
  formatInterval( seconds ) {
    const value = Number( seconds );
    if ( !Number.isFinite( value ) || value <= 0 ) return "";
    const minutes = value / 60;
    // Never round down to zero: the shortest delay the scheduler produces is
    // ten minutes, and a button reading "0m" would be nonsense.
    if ( minutes < 60 ) return I18n.format( "train.interval_minutes" , { count: Math.max( 1 , Math.round( minutes ) ) } );
    const hours = minutes / 60;
    if ( hours < 24 ) return I18n.format( "train.interval_hours" , { count: Math.round( hours ) } );
    const days = hours / 24;
    if ( days < 30 ) return I18n.format( "train.interval_days" , { count: Math.round( days ) } );
    const months = days / 30.44;
    if ( months < 12 ) return I18n.format( "train.interval_months" , { count: Math.round( months ) } );
    return I18n.format( "train.interval_years" , { count: Math.round( days / 365 ) } );
  },

  renderProgress() {
    // Both of these measure one run, and the endless deck has no runs in it:
    // a bar that filled up and reset every twenty cards would be counting
    // something the user has explicitly asked not to have. The totals beside
    // them stay, because those are about the words rather than the sitting.
    const endless = this.endless();
    Dom.show( Dom.get( "deck-progress" ) , !endless );
    Dom.show( Dom.get( "deck-remaining" ) , !endless );
    if ( endless ) return;

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
    node.appendChild( this.buildTop( card , revealed ) );
    node.appendChild( this.buildFace( card , revealed ) );
    const foot = this.buildFoot();
    if ( foot ) node.appendChild( foot );
    return node;
  },

  buildTop( card , revealed ) {
    const top = Dom.el( "div" , { class: "card-top" } );
    top.appendChild( this.buildBadges( card ) );
    const self = this;
    [ "left" , "right" , "up" ].forEach( function ( direction ) {
      const hint = self.buildHint( direction );
      if ( hint ) top.appendChild( hint );
    } );

    // The speaker sits in the corner of the top band rather than beside the
    // word: next to display type it competes with the one thing on the card
    // the reader is supposed to be looking at. It is built only when the word
    // is actually on screen -- in "show the meaning, hide the word" mode a
    // button on a face-down card would offer to read out the answer.
    if ( this.wordIsShowing( revealed ) ) {
      const speak = Speech.button( card.word );
      if ( speak ) top.appendChild( speak );
    }
    return top;
  },

  wordIsShowing( revealed ) {
    return this.settings.reveal_mode !== "word_hidden" || !!revealed;
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
  //
  // What it says has to match the button the same gesture answers: with the
  // grades on, the "don't know" direction is the Again button, so the hint
  // reads Again rather than "Not known". The colour still follows the outcome
  // itself, which is why the class and the key are looked up separately.
  buildHint( direction ) {
    const outcome = this.outcomeFor[ direction ];
    if ( !outcome ) return null;
    const named = this.graded() && outcome === "unknown" ? "again" : outcome;
    return Dom.el( "div" , {
      class: "swipe-hint swipe-hint-" + outcome,
      text: I18n.get( "train.hint_" + named ),
      attrs: { "data-direction": direction },
    } );
  },

  buildBadges( card ) {
    const badges = Dom.el( "div" , { class: "card-badges" } );
    badges.appendChild( Dom.el( "span" , {
      class: "badge " + ( card.is_new ? "badge-new" : "badge-review" ),
      text: I18n.get( card.is_new ? "train.new_badge" : "train.review_badge" ),
    } ) );
    // The reading level is the same on nearly every card in a run -- it is
    // what the run was drawn from -- so it is the first thing to go for
    // anyone who finds the top of the card busy. On by default: it is the
    // only place the level is visible while training.
    if ( !this.settings.hide_level_badge ) {
      badges.appendChild( Dom.el( "span" , { class: "badge" , text: Shell.levelName( card.tier ) } ) );
    }
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

  // --- gestures ----------------------------------------------------------

  attachGestures( node ) {
    const self = this;
    Swipe.attach( node , {
      allows( direction ) { return !!self.outcomeFor[ direction ]; },
      onMove( dx , dy , reach ) { self.followFinger( node , dx , dy , reach ); },
      onScroll( scroller ) { Fit.markScroll( scroller ); },
      onCancel() { self.settle( node ); },
      onTap() { self.handleTap(); },
      onCommit( direction ) { self.handleOutcome( direction , node ); },
    } );
  },

  // reach is how far this card has to travel on each axis to commit, which
  // depends on how big the card ended up -- so the hints are read against the
  // same number the release is judged by rather than a constant that only
  // matched on the screen it was written for.
  followFinger( node , dx , dy , reach ) {
    node.classList.remove( "is-animating" );
    // A small rotation tied to horizontal travel is what makes the card feel
    // like a physical object rather than a sliding rectangle.
    const tilt = dx / 18;
    node.style.transform = "translate(" + dx + "px, " + dy + "px) rotate(" + tilt + "deg)";

    const progress = function ( value , distance ) {
      return Math.max( 0 , Math.min( 1 , Math.abs( value ) / ( distance || Swipe.DISTANCE ) ) );
    };
    const span = reach || { x: Swipe.DISTANCE , y: Swipe.DISTANCE };
    const strength = {
      right: dx > 0 ? progress( dx , span.x ) : 0,
      left:  dx < 0 ? progress( dx , span.x ) : 0,
      up:    dy < 0 ? progress( dy , span.y ) : 0,
      down:  dy > 0 ? progress( dy , span.y ) : 0,
    };

    const self = this;
    let loudest = 0;
    Object.keys( strength ).forEach( function ( direction ) {
      self.setHint( node , direction , self.hintOpacity( strength[ direction ] ) );
      if ( strength[ direction ] > loudest ) loudest = strength[ direction ];
    } );
    // The badges and the speaker share the top band with three of the hints,
    // so the band is handed from one to the other rather than shared: both
    // have faded out by the point the first hint fades in, and nothing is
    // ever drawn over anything else at any distance.
    this.setTopOpacity( node , Math.max( 0 , 1 - loudest / this.HANDOVER ) );
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

  // Everything in the top band that is not a hint. The speaker goes with the
  // badges because it sits where the rightward hint arrives.
  setTopOpacity( node , opacity ) {
    Dom.all( ".card-badges, .card-speak" , node ).forEach( function ( element ) {
      element.style.opacity = String( opacity );
    } );
  },

  settle( node ) {
    node.classList.add( "is-animating" );
    node.style.transform = "";
    const self = this;
    [ "left" , "right" , "up" , "down" ].forEach( function ( direction ) {
      self.setHint( node , direction , 0 );
    } );
    this.setTopOpacity( node , 1 );
  },

  flyAway( node , direction ) {
    node.classList.add( "is-animating" , "is-gone" );
    const offsets = {
      right: "translate(120vw, 0) rotate(22deg)",
      left:  "translate(-120vw, 0) rotate(-22deg)",
      up:    "translate(0, -110vh) rotate(0deg)",
      down:  "translate(0, 110vh) rotate(0deg)",
      // Hard and Good have no direction on them, so the card shrinks away
      // where it stands instead of borrowing a neighbour's exit. Sending it
      // left or right would say the card had been swiped that way, and the
      // next thing the user tries is that swipe.
      none:  "scale(0.88)",
    };
    node.style.transform = offsets[ direction ] || offsets.none;
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

  // handleOutcome takes the direction a gesture or an arrow key arrived on
  // and turns it into the answer it is bound to. A direction nothing is bound
  // to is dropped here rather than deeper down, which is what keeps the
  // fourth direction inert.
  handleOutcome( direction , node ) {
    if ( this.awaitingContinue ) {
      this.advance();
      return;
    }
    const outcome = this.outcomeFor[ direction ];
    if ( !outcome ) return;
    this.answer( outcome , node );
  },

  // answer is the one place a swipe, a button press and a key press all end
  // up, so the input methods cannot drift apart. It takes the outcome rather
  // than the direction because two of the grades have no direction: Hard and
  // Good are reachable only from a button or a number key.
  async answer( outcome , node ) {
    // Once the answer is showing, the card has already been recorded. Any
    // further input means "move on" rather than a second opinion.
    if ( this.awaitingContinue ) {
      this.advance();
      return;
    }
    if ( this.busy ) return;

    const card = this.current();
    if ( !card || !outcome ) return;

    // Null for Hard and Good, which flyAway reads as "leave without going
    // anywhere". Everything else exits the way the thumb would have sent it,
    // whether or not a thumb was involved.
    const direction = this.directionOf( outcome );

    this.busy = true;
    // A miss gets the longest buzz and a struggle the middling one: the
    // feedback is a readout of the answer, so it should not be flat across
    // answers that are not.
    this.busyBuzz( outcome );

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

    // Four ways a card leaves immediately: a skip, which never reveals
    // because setting a word aside means not engaging with it at all; a card
    // already turned over, whose answer has been read; a hold of zero, which
    // is the user saying they would rather not be shown the answer they did
    // not ask for; and an answer that says the word was known, when those
    // have been excused from the reveal in the settings.
    if ( outcome === "skip" || this.revealed || hold === 0 || this.excusedFromReveal( outcome ) ) {
      if ( top ) this.flyAway( top , direction );
      window.setTimeout( function () { self.advance(); } , 180 );
      return;
    }

    // Not yet revealed: bring the card back to the middle and turn it over,
    // so the answer is read before it leaves.
    this.revealed = true;
    this.awaitingContinue = true;
    this.pendingExit = true;
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

    const stack = Dom.get( "card-stack" );
    if ( this.pendingExit && stack && stack.lastElementChild ) {
      // A null direction is the point of the flag: a card answered Hard or
      // Good still has to leave, it just has nowhere in particular to go.
      this.flyAway( stack.lastElementChild , this.pendingDirection );
    }

    this.at += 1;
    this.revealed = false;
    this.awaitingContinue = false;
    this.pendingExit = false;
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
    // Every button is bound to an outcome, never to a direction: the
    // direction is looked up when the button is pressed, so changing the
    // mapping -- or turning the grades on, which changes what the rightward
    // one means -- never leaves a stale button behind.
    //
    // Both rows are wired once here even though only one of them is ever on
    // screen. A hidden button cannot be clicked, and binding on every render
    // is how a handler ends up attached twice.
    const wire = function ( id , outcome ) {
      const button = Dom.get( id );
      if ( !button ) return;
      button.addEventListener( "click" , function () {
        const stack = Dom.get( "card-stack" );
        self.answer( outcome , stack ? stack.lastElementChild : null );
      } );
    };
    wire( "action-unknown" , "unknown" );
    wire( "action-skip" , "skip" );
    wire( "action-known" , "known" );

    this.gradeButtons.forEach( function ( entry ) { wire( entry.id , entry.outcome ); } );

    const reveal = Dom.get( "action-reveal" );
    if ( reveal ) reveal.addEventListener( "click" , function () { self.handleTap(); } );

    const more = Dom.get( "deck-more" );
    if ( more ) more.addEventListener( "click" , function () { self.loadMore(); } );
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

      // Nor from a button on the card itself -- the one that says the word
      // out loud. Space and Enter are how a button is pressed from the
      // keyboard, and taking them here first means the press is swallowed:
      // preventDefault stops the click, and the card turns over instead of
      // the word being read. The answer buttons below the card are not on it
      // and are unaffected.
      if ( tag === "BUTTON" && event.target.closest( ".swipe-card" ) ) return;

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

      // 1 to 4 across the grades, soonest first, which is what Anki does and
      // what anyone arriving from it will try. They are the only way to reach
      // Hard and Good without a mouse, so they exist for more than habit.
      if ( self.graded() ) {
        const slot = event.key.length === 1 ? "1234".indexOf( event.key ) : -1;
        if ( slot !== -1 ) {
          event.preventDefault();
          self.answer( self.gradeButtons[ slot ].outcome , top );
          return;
        }
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
