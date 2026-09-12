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
const Swipe = {
  // A swipe commits when it passes this far, or when it is flicked faster
  // than this regardless of distance. The flick check is what makes a quick
  // short gesture feel responsive instead of snapping back.
  DISTANCE: 78,
  VELOCITY: 0.55, // px per millisecond

  // Movement below this is a tap, not a drag. Fingers wobble; a strict zero
  // would make tapping unreliable on a phone.
  TAP_SLOP: 10,
  TAP_TIME: 500,

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
    };

    const finish = function () {
      state.active = false;
      state.pointerId = null;
      state.axis = null;
      state.dx = 0;
      state.dy = 0;
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
      }

      // Travel along an axis the gesture has not taken, or toward a direction
      // nothing is bound to, is flattened to zero so the card only ever
      // follows a drag that can actually commit.
      let dx = state.axis === "y" ? 0 : state.dx;
      let dy = state.axis === "x" ? 0 : state.dy;
      if ( allows( dx > 0 ? "right" : "left" ) === false ) dx = 0;
      if ( allows( dy > 0 ? "down" : "up" ) === false ) dy = 0;
      if ( handlers.onMove ) handlers.onMove( dx , dy );
    } );

    const release = function ( event ) {
      if ( !state.active || event.pointerId !== state.pointerId ) return;
      const elapsed = ( event.timeStamp || Date.now() ) - state.startedAt;
      const dx = state.dx;
      const dy = state.dy;
      const axis = state.axis;
      try { element.releasePointerCapture( event.pointerId ); } catch ( releaseError ) { /* already gone */ }
      finish();

      const travelled = Math.abs( dx ) + Math.abs( dy );
      if ( travelled < Swipe.TAP_SLOP && elapsed < Swipe.TAP_TIME ) {
        if ( handlers.onTap ) handlers.onTap();
        return;
      }

      const travel = axis === "x" ? dx : dy;
      const speed = elapsed > 0 ? Math.abs( travel ) / elapsed : 0;
      const direction = axis === "x"
        ? ( dx > 0 ? "right" : "left" )
        : ( dy > 0 ? "down" : "up" );

      if ( axis && allows( direction ) && ( Math.abs( travel ) > Swipe.DISTANCE || speed > Swipe.VELOCITY ) ) {
        if ( handlers.onCommit ) handlers.onCommit( direction );
        return;
      }
      if ( handlers.onCancel ) handlers.onCancel();
    };

    element.addEventListener( "pointerup" , release );
    element.addEventListener( "pointercancel" , function ( event ) {
      if ( !state.active || event.pointerId !== state.pointerId ) return;
      finish();
      if ( handlers.onCancel ) handlers.onCancel();
    } );

    // A drag that starts on a card and ends over an image would otherwise
    // become a native drag halfway through and freeze the animation.
    element.addEventListener( "dragstart" , function ( event ) { event.preventDefault(); } );
  },
};
