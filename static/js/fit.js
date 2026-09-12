// Scaling text down so it fits the box it was given.
//
// This exists because of one specific bad outcome. The word on a card is the
// thing the reader is being asked to recognise, and the browser's answer to a
// word wider than the card is to break it in half:
//
//     incomprehensi
//     bility
//
// Two fragments, neither of which is the word. Hyphenation is better but not
// much -- it still asks the reader to reassemble what they were meant to read
// at a glance. So the type is measured against the space available and scaled
// until the word fits on one line, and only if it cannot fit at a size that
// is still readable does wrapping come back into it.
//
// Sizes are written through element.style.fontSize -- the CSSOM property,
// which the page's CSP allows -- and never through setAttribute("style"),
// which it does not. Same rule as the transforms in train.js.
const Fit = {
  // Never scale below this many pixels, whatever it takes to fit. Past here
  // the text has stopped being easier to read than a wrapped version of
  // itself, and wrapping is the better trade.
  FLOOR: 17,

  // How far below its CSS size a piece of text may be scaled. A ceiling of
  // 46px bottoms out around 19px; the FLOOR above still applies.
  SHRINK_TO: 0.42,

  // A hair off every computed size. Text measurement is done in whole
  // subpixels and letter-spacing is applied after the last glyph, so a
  // width that measures as exactly equal can still wrap.
  SAFETY: 0.985,

  // The stylesheet's own size for this element, with any previous pass's
  // answer thrown away first. This is where a fit starts from.
  ceilingOf( element ) {
    element.style.fontSize = "";
    return parseFloat( window.getComputedStyle( element ).fontSize ) || 16;
  },

  // The size to carry on from. A second pass over the same element -- fitting
  // the height of a word whose width has already been fitted -- must not
  // reset to the stylesheet's size, or it undoes the pass before it and the
  // word goes back to being too wide.
  startOf( element ) {
    const inline = parseFloat( element.style.fontSize );
    if ( Number.isFinite( inline ) && inline > 0 ) return inline;
    return this.ceilingOf( element );
  },

  // Scale element's text down until it sits on a single line, and say whether
  // that worked. A caller that gets false back has text which could not fit
  // at a readable size; the element is left at its smallest with wrapping
  // allowed, which is the least bad remaining option.
  //
  // The is-measuring class is what makes this measurable at all: while it is
  // on, the text may not wrap and may not overflow, so scrollWidth is the
  // width the text wants and clientWidth is the width it has. Without it a
  // too-long word simply wraps and both numbers agree, leaving nothing to
  // scale by.
  toOneLine( element ) {
    if ( !element ) return true;
    element.classList.remove( "fits-one-line" );
    const ceiling = this.ceilingOf( element );
    const floor = Math.max( this.FLOOR , ceiling * this.SHRINK_TO );

    element.classList.add( "is-measuring" );

    let size = ceiling;
    let fits = true;
    // Three passes is enough: each one lands within a percent or so, and the
    // next only corrects for rounding. A loop that ran to convergence would
    // force the same number of reflows for no visible gain.
    for ( let pass = 0; pass < 3; pass += 1 ) {
      const available = element.clientWidth;
      const wanted = element.scrollWidth;
      if ( available <= 0 ) break;
      if ( wanted <= available ) { fits = true; break; }

      const scaled = size * ( available / wanted ) * this.SAFETY;
      fits = scaled >= floor;
      size = Math.max( floor , scaled );
      element.style.fontSize = size.toFixed( 2 ) + "px";
    }

    element.classList.remove( "is-measuring" );
    // Only a line that is genuinely on one line is pinned to one line. Doing
    // this the other way round -- leaving nowrap on regardless -- is how you
    // get a word running out past the edge of the card.
    if ( fits ) element.classList.add( "fits-one-line" );
    return fits;
  },

  // A line box is laid out in fractions of a pixel and reported in whole
  // ones, so a box holding exactly one line of text routinely measures a
  // pixel or two taller than the room it has. Anything under this is that,
  // not overflow.
  SLACK: 2,

  // Scale element's text down until container has stopped overflowing.
  // Separate from toOneLine because the thing that overflows is usually not
  // the thing whose size has to change: a long definition overflows the card
  // half it sits in, not the paragraph, which grows to fit its own text.
  toContainer( element , container , shrinkTo ) {
    if ( !element || !container ) return true;
    const overflow = function () { return container.scrollHeight - container.clientHeight; };
    if ( overflow() <= this.SLACK ) return true;

    const start = this.startOf( element );
    const floor = Math.max( this.FLOOR , start * ( shrinkTo || this.SHRINK_TO ) );

    let size = start;
    let over = overflow();
    let guard = 0;
    while ( over > this.SLACK && size > floor && guard < 14 ) {
      const next = Math.max( floor , size * 0.93 );
      element.style.fontSize = next.toFixed( 2 ) + "px";
      const now = overflow();

      // Shrinking only helps when the container's height is fixed by
      // something other than the text inside it. A container that sizes
      // itself to its contents gets shorter in step with the type, so the
      // gap never closes and the loop would chase a pixel of rounding all
      // the way down to the floor -- which is how the same word came out at
      // two different sizes depending on where the rounding happened to
      // land. A step that did not reduce the overflow was the wrong lever:
      // put the size back and stop.
      if ( now >= over ) {
        element.style.fontSize = size.toFixed( 2 ) + "px";
        break;
      }

      size = next;
      over = now;
      guard += 1;
    }
    return overflow() <= this.SLACK;
  },

  // Mark a scrolling box so the reader can tell there is more below it: the
  // stylesheet fades the last few millimetres of a box that can still move.
  // Called once after layout and again on every scroll, because the fade has
  // to go away at the bottom or it reads as text that is permanently cut off.
  markScroll( element ) {
    if ( !element ) return;
    const overflows = element.scrollHeight - element.clientHeight > this.SLACK;
    element.classList.toggle( "is-scrollable" , overflows );
    element.classList.toggle(
      "is-at-end",
      !overflows || element.scrollTop >= element.scrollHeight - element.clientHeight - 1
    );
  },
};
