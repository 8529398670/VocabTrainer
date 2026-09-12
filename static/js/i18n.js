// Pulls language.yaml (as JSON) from the server and stamps it into the page.
//
// The contract with language.yaml is what makes this more than a translation
// layer: a key that is missing or empty means "do not show this element". So
// removing a label, a button, or an entire section is an edit to the YAML,
// not to the markup.
//
// Note the asymmetry in apply(): a missing key hides its element, but a
// present key never un-hides one. Plenty of elements (error banners, the
// admin panel) are hidden by app.js based on state, and i18n has no business
// overruling that -- it decides what text says, not what the app reveals.
const I18n = {
  data: {},

  async load() {
    const response = await fetch( "/api/language" , { credentials: "same-origin" } );
    if ( !response.ok ) throw new Error( "could not load language file" );
    this.data = await response.json();
    return this.data;
  },

  // Resolve a dotted key like "admin.create_button".
  get( key ) {
    let cursor = this.data;
    const parts = String( key ).split( "." );
    for ( let index = 0; index < parts.length; index += 1 ) {
      if ( cursor == null || typeof cursor !== "object" ) return "";
      cursor = cursor[ parts[ index ] ];
    }
    return typeof cursor === "string" ? cursor : "";
  },

  // Same lookup, with {placeholders} filled in:
  //
  //   I18n.format( "settings.swipe_unused" , { direction: "down" } )
  //
  // A placeholder with no value is left as it is rather than becoming
  // "undefined", so a translation that uses a name this call does not supply
  // shows the gap instead of a lie. An unknown key still returns "", which
  // keeps the hide-an-empty-key rule working for formatted strings too.
  format( key , values ) {
    const template = this.get( key );
    if ( template === "" ) return "";
    const supplied = values || {};
    return template.replace( /\{(\w+)\}/g , function ( placeholder , name ) {
      return Object.prototype.hasOwnProperty.call( supplied , name )
        ? String( supplied[ name ] )
        : placeholder;
    } );
  },

  apply( root ) {
    const scope = root || document;

    Dom.all( "[data-i18n]" , scope ).forEach( function ( element ) {
      const value = I18n.get( element.getAttribute( "data-i18n" ) );
      if ( value === "" ) {
        element.hidden = true;
        return;
      }
      if ( element.tagName === "TITLE" ) {
        document.title = value;
        return;
      }
      element.textContent = value;
    } );

    // data-i18n-attr="placeholder:account.name_placeholder,title:some.key"
    Dom.all( "[data-i18n-attr]" , scope ).forEach( function ( element ) {
      element.getAttribute( "data-i18n-attr" ).split( "," ).forEach( function ( pair ) {
        const halves = pair.split( ":" );
        if ( halves.length !== 2 ) return;
        const value = I18n.get( halves[ 1 ].trim() );
        if ( value !== "" ) element.setAttribute( halves[ 0 ].trim() , value );
      } );
    } );
  },
};
