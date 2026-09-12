// Pointer-driven swipe gestures for a card.
//
// No app logic here: this file turns finger movement into one of five
// outcomes -- left, right, up, down, or tap -- and reports how far the
// current drag has got so the caller can show it. What those outcomes mean is
// train.js's business.
//
// Which directions are live is the caller's to decide, because the app lets
// the user move each answer onto whichever direction suits their hand. A
// direction that is not live does not move the card at all: animating toward
// a gesture that will not fire is a promise the release then breaks.
//
// Pointer events rather than touch events, so a mouse on a desktop and a
// finger on a phone go down the same path and there is only one code path to
// get right.
//
// The card sets touch-action: none, which means the browser hands over every
// gesture that starts on it and takes none of them back. That is the whole
// reason the page underneath stays still. The cost is that scrolling the
// card's own overflowing text is now this file's job too -- see scrollerUnder
// and the "scroll" mode -- because a browser that has been told not to claim
// the gesture will not scroll anything either.
const Swipe = {
  // A swipe commits when it passes this far, or when it is flicked faster
  // than this regardless of distance. The flick check is what makes a quick
  // short gesture feel responsive instead of snapping back.
  DISTANCE: 78,
  VELOCITY: 0.55, // px per millisecond

  // On a short screen -- a phone on its side -- a fixed 78px is a quarter of
  // the way across the card and a vertical swipe starts to feel like a chore.
  // The distance that commits is therefore capped at a share of the card, so
  // a smaller card asks for a smaller gesture.
  SHARE: 0.26,
  MINIMUM: 38,

  // Movement below this is a tap, not a drag. Fingers wobble; a strict zero
  // would make tapping unreliable on a phone.
  TAP_SLOP: 10,
  TAP_TIME: 500,

  // How far a swipe has to travel on each axis to commit, for this card at
  // its current size. Handed to onMove so the caller's readout and the rule
  // that actually fires are the same number.
  reachOf( element ) {
    const clamp = function ( size ) {
      return Math.max( Swipe.MINIMUM , Math.min( Swipe.DISTANCE , size * Swipe.SHARE ) );
    };
    return {
      x: clamp( element.clientWidth || Swipe.DISTANCE / Swipe.SHARE ),
      y: clamp( element.clientHeight || Swipe.DISTANCE / Swipe.SHARE ),
    };
  },

  // The nearest thing between the touched element and the card that both can
  // scroll and has somewhere to scroll to. Used to decide, once, whether a
  // vertical drag is reading the card or answering it.
  scrollerUnder( target , root ) {
    let node = target;
    while ( node && node !== root.parentNode ) {
      if ( node.nodeType === 1 && node.scrollHeight > node.clientHeight + 1 ) {
        const overflow = window.getComputedStyle( node ).overflowY;
        if ( overflow === "auto" || overflow === "scroll" ) return node;
      }
      node = node.parentNode;
    }
    return null;
  },

  // handlers.allows( direction ) says whether a direction can commit. Leave it
  // out and all four do.
  attach( element , handlers ) {
    const allows = function ( direction ) {
      return handlers.allows ? !!handlers.allows( direction ) : true;
    };

    const state = {
      active: false,
      pointerId: null,
      startX: 0, startY: 0,
      dx: 0, dy: 0,
      startedAt: 0,
      axis: null,
      reach: { x: Swipe.DISTANCE , y: Swipe.DISTANCE },
      // The scroller the gesture began over, if any, and the scroll position
      // it had at the time. Both only matter until the axis is settled.
      scroller: null,
      scrollFrom: 0,
      // "card" or "scroll", decided once when the axis locks and never
      // revisited: a gesture that changes its mind halfway through is worse
      // than either answer.
      mode: null,
    };

    const finish = function () {
      state.active = false;
      state.pointerId = null;
      state.axis = null;
      state.dx = 0;
      state.dy = 0;
      state.scroller = null;
      state.mode = null;
    };

    // Whether the scroller the finger is over can still move the way the
    // finger is going. Dragging down shows earlier text, which means a
    // smaller scrollTop; dragging up, a larger one. At either end there is
    // nothing to read in that direction, so the gesture belongs to the card.
    const canScroll = function ( dy ) {
      const node = state.scroller;
      if ( !node ) return false;
      if ( dy > 0 ) return node.scrollTop > 0;
      return node.scrollTop < node.scrollHeight - node.clientHeight - 1;
    };

    element.addEventListener( "pointerdown" , function ( event ) {
      if ( state.active ) return;
      if ( event.button !== undefined && event.button !== 0 ) return;
      state.active = true;
      state.pointerId = event.pointerId;
      state.startX = event.clientX;
      state.startY = event.clientY;
      state.dx = 0;
      state.dy = 0;
      state.startedAt = event.timeStamp || Date.now();
      state.axis = null;
      state.mode = null;
      state.reach = Swipe.reachOf( element );
      state.scroller = Swipe.scrollerUnder( event.target , element );
      state.scrollFrom = state.scroller ? state.scroller.scrollTop : 0;
      // Capture means the rest of the gesture keeps arriving here even if the
      // finger leaves the card, which it usually does on a long swipe.
      try { element.setPointerCapture( event.pointerId ); } catch ( captureError ) { /* not fatal */ }
      if ( handlers.onStart ) handlers.onStart();
    } );

    element.addEventListener( "pointermove" , function ( event ) {
      if ( !state.active || event.pointerId !== state.pointerId ) return;
      state.dx = event.clientX - state.startX;
      state.dy = event.clientY - state.startY;

      // Lock to one axis once the gesture has clearly picked a direction, so
      // a sloppy horizontal swipe does not also read as a skip.
      if ( !state.axis && Math.abs( state.dx ) + Math.abs( state.dy ) > Swipe.TAP_SLOP ) {
        state.axis = Math.abs( state.dx ) >= Math.abs( state.dy ) ? "x" : "y";
        // Settled here and only here: a vertical drag that began over text
        // with more text above or below it is reading, not answering.
        state.mode = ( state.axis === "y" && canScroll( state.dy ) ) ? "scroll" : "card";
      }

      if ( state.mode === "scroll" ) {
        state.scroller.scrollTop = state.scrollFrom - state.dy;
        if ( handlers.onScroll ) handlers.onScroll( state.scroller );
        return;
      }

      // Nothing moves until the gesture has said what it is. The alternative
      // -- following the finger through the slop and flattening afterwards --
      // leaves the card nudged a few pixels off centre by a drag that turned
      // out to be a scroll, with no release to put it back. Ten pixels is a
      // couple of milliseconds of a real finger, so there is nothing to feel.
      if ( !state.axis ) return;

      // Travel along an axis the gesture has not taken, or toward a direction
      // nothing is bound to, is flattened to zero so the card only ever
      // follows a drag that can actually commit.
      let dx = state.axis === "y" ? 0 : state.dx;
      let dy = state.axis === "x" ? 0 : state.dy;
      if ( allows( dx > 0 ? "right" : "left" ) === false ) dx = 0;
      if ( allows( dy > 0 ? "down" : "up" ) === false ) dy = 0;
      if ( handlers.onMove ) handlers.onMove( dx , dy , state.reach );
    } );

    const release = function ( event ) {
      if ( !state.active || event.pointerId !== state.pointerId ) return;
      const elapsed = ( event.timeStamp || Date.now() ) - state.startedAt;
      const dx = state.dx;
      const dy = state.dy;
      const axis = state.axis;
      const mode = state.mode;
      const reach = state.reach;
      try { element.releasePointerCapture( event.pointerId ); } catch ( releaseError ) { /* already gone */ }
      finish();

      const travelled = Math.abs( dx ) + Math.abs( dy );
      if ( travelled < Swipe.TAP_SLOP && elapsed < Swipe.TAP_TIME ) {
        if ( handlers.onTap ) handlers.onTap();
        return;
      }

      // A drag that spent itself scrolling the card's text never moved the
      // card and must not answer it.
      if ( mode === "scroll" ) return;

      const travel = axis === "x" ? dx : dy;
      const distance = axis === "x" ? reach.x : reach.y;
      const speed = elapsed > 0 ? Math.abs( travel ) / elapsed : 0;
      const direction = axis === "x"
        ? ( dx > 0 ? "right" : "left" )
        : ( dy > 0 ? "down" : "up" );

      if ( axis && allows( direction ) && ( Math.abs( travel ) > distance || speed > Swipe.VELOCITY ) ) {
        if ( handlers.onCommit ) handlers.onCommit( direction );
        return;
      }
      if ( handlers.onCancel ) handlers.onCancel();
    };

    element.addEventListener( "pointerup" , release );

    const abandon = function ( event ) {
      if ( !state.active || event.pointerId !== state.pointerId ) return;
      const wasScrolling = state.mode === "scroll";
      finish();
      if ( !wasScrolling && handlers.onCancel ) handlers.onCancel();
    };

    element.addEventListener( "pointercancel" , abandon );
    // Losing capture without an up or a cancel happens on iOS when the system
    // takes over -- an incoming call, the app switcher, a control-centre
    // drag. Without this the card is left wherever the finger was when the
    // interruption arrived, and only a re-render puts it back.
    element.addEventListener( "lostpointercapture" , abandon );

    // A drag that starts on a card and ends over an image would otherwise
    // become a native drag halfway through and freeze the animation.
    element.addEventListener( "dragstart" , function ( event ) { event.preventDefault(); } );

    // Belt and braces for iOS, where the long-press callout and the selection
    // magnifier can still appear over a card despite -webkit-touch-callout
    // and user-select being off in CSS.
    element.addEventListener( "contextmenu" , function ( event ) { event.preventDefault(); } );
  },
};
